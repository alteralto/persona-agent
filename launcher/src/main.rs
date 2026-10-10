// Пульт Пыхтуна: start, stop and watch everything the character needs (Minecraft, Laya, the
// voice service, the agent) without a terminal. Services keep running when the window closes.
#![windows_subsystem = "windows"]

use std::fs;
use std::io::{Read, Seek, SeekFrom};
use std::net::{SocketAddr, TcpStream};
use std::os::windows::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

use eframe::egui::{self, Color32, RichText};

const NO_WINDOW: u32 = 0x0800_0000; // CREATE_NO_WINDOW: no console flashes for services
const LAUNCHER: &str = r"F:\.tlauncher\legacy\Minecraft\LL.exe";
const LAYA: &str = r"F:\laya\.venv-gpu\Scripts\laya-serve.exe";
const BASH: &str = r"C:\Program Files\Git\bin\bash.exe";
const PORT_MOD: u16 = 25350;
const PORT_LAYA: u16 = 8090;
const PORT_VOICE: u16 = 8091;
const PORT_AGENT: u16 = 8092; // the agent's /voice endpoint: up while the agent runs

fn main() -> eframe::Result {
    let options = eframe::NativeOptions {
        viewport: egui::ViewportBuilder::default()
            .with_title("Пыхтун — пульт")
            .with_inner_size([860.0, 640.0])
            .with_min_inner_size([520.0, 420.0]),
        ..Default::default()
    };
    eframe::run_native("pult", options, Box::new(|cc| Ok(Box::new(Pult::new(cc)))))
}

/// What is up right now; filled by a background thread every 1.5 s.
#[derive(Clone, Default)]
struct Status {
    game: bool,  // the game (not the launcher) is running
    modon: bool, // the fork's MCP answers: the agent can drive the game
    laya: bool,
    voice: bool,
    agent: bool,
    checked: bool,
}

/// Settings kept between runs in data/pult.ini.
#[derive(Clone)]
struct Settings {
    test_mode: bool, // flight testing: long legs out from home and back
    home_x: String,
    home_z: String,
    laya_cpu: bool, // Laya on the CPU leaves the whole GPU to the game
}

impl Default for Settings {
    fn default() -> Self {
        Settings { test_mode: false, home_x: "-471".into(), home_z: "-183".into(), laya_cpu: false }
    }
}

#[derive(Clone, Copy, PartialEq)]
enum LogTab {
    Agent,
    Laya,
    Voice,
}

struct Shared {
    status: Status,
    busy: Option<String>, // a start or stop under way
    note: String,         // the last outcome, shown under the buttons
}

struct Pult {
    root: PathBuf,
    shared: Arc<Mutex<Shared>>,
    settings: Settings,
    tab: LogTab,
    log_lines: Vec<String>,
    log_read: Instant,
    only_flight: bool,
}

impl Pult {
    fn new(cc: &eframe::CreationContext<'_>) -> Self {
        setup_fonts(&cc.egui_ctx);
        let root = find_root();
        let shared = Arc::new(Mutex::new(Shared { status: Status::default(), busy: None, note: String::new() }));
        {
            let shared = shared.clone();
            let ctx = cc.egui_ctx.clone();
            // Ports every 1.5 s cost nothing; the game window (a process listing) every 7.5 s.
            thread::spawn(move || {
                let mut game = false;
                for n in 0u64.. {
                    if n % 5 == 0 {
                        game = game_running();
                    }
                    let st = poll_status(game);
                    shared.lock().unwrap().status = st;
                    ctx.request_repaint();
                    thread::sleep(Duration::from_millis(1500));
                }
            });
        }
        let settings = load_settings(&root);
        Pult {
            root,
            shared,
            settings,
            tab: LogTab::Agent,
            log_lines: Vec::new(),
            log_read: Instant::now() - Duration::from_secs(10),
            only_flight: false,
        }
    }

    /// Run a slow action (start, stop) off the UI thread; one at a time.
    fn run(&self, ctx: &egui::Context, what: &str, job: impl FnOnce(&Path, &Settings) -> String + Send + 'static) {
        let mut sh = self.shared.lock().unwrap();
        if sh.busy.is_some() {
            return;
        }
        sh.busy = Some(what.to_string());
        drop(sh);
        save_settings(&self.root, &self.settings);
        let (shared, root, settings, ctx) = (self.shared.clone(), self.root.clone(), self.settings.clone(), ctx.clone());
        thread::spawn(move || {
            let note = job(&root, &settings);
            let st = poll_status(game_running());
            let mut sh = shared.lock().unwrap();
            sh.busy = None;
            sh.note = note;
            sh.status = st;
            ctx.request_repaint();
        });
    }

    fn read_log(&mut self) {
        if self.log_read.elapsed() < Duration::from_millis(1000) {
            return;
        }
        self.log_read = Instant::now();
        let file = match self.tab {
            LogTab::Agent => "pyhtun.log",
            LogTab::Laya => "laya.err.log",
            LogTab::Voice => "voice.err.log",
        };
        let text = tail(&self.root.join("logs").join(file), 96 * 1024);
        let mut lines: Vec<String> = text
            .lines()
            .map(tidy_line)
            .filter(|l| !self.only_flight || l.contains("flight") || l.contains("fly") || l.contains("hands"))
            .collect();
        if lines.len() > 400 {
            lines.drain(..lines.len() - 400);
        }
        self.log_lines = lines;
    }
}

impl eframe::App for Pult {
    fn ui(&mut self, ui: &mut egui::Ui, _frame: &mut eframe::Frame) {
        let ctx = ui.ctx().clone();
        ctx.request_repaint_after(Duration::from_secs(1)); // the log keeps moving
        self.read_log();
        let (st, busy, note) = {
            let sh = self.shared.lock().unwrap();
            (sh.status.clone(), sh.busy.clone(), sh.note.clone())
        };
        let free = busy.is_none();

        egui::Panel::top("controls").show(ui, |ui| {
            ui.add_space(8.0);
            ui.heading("Пыхтун");
            ui.add_space(4.0);
            egui::Grid::new("status").num_columns(3).spacing([16.0, 6.0]).show(ui, |ui| {
                let (up, text) = if st.modon {
                    (true, "в игре, мод на связи")
                } else if st.game {
                    (false, "запущен, мод не отвечает — зайди в мир")
                } else {
                    (false, "не запущен")
                };
                row(ui, st.checked, "Minecraft", up, text);
                if ui.add_enabled(!st.game && free, egui::Button::new("Открыть лаунчер")).clicked() {
                    self.run(&ctx, "Открываю лаунчер", |_, _| start_minecraft());
                }
                ui.end_row();

                row(ui, st.checked, "Laya (решения)", st.laya, if st.laya { "работает" } else { "выключена" });
                self.service_buttons(ui, &ctx, free, st.laya, "Laya", start_laya, stop_laya);
                ui.end_row();

                row(ui, st.checked, "Голос", st.voice, if st.voice { "работает" } else { "выключен" });
                self.service_buttons(ui, &ctx, free, st.voice, "голос", start_voice, stop_voice);
                ui.end_row();

                row(ui, st.checked, "Агент", st.agent, if st.agent { "работает" } else { "выключен" });
                ui.horizontal(|ui| {
                    if ui.add_enabled(free && !st.agent, egui::Button::new("Запустить")).clicked() {
                        self.run(&ctx, "Запускаю агента", start_agent);
                    }
                    if ui.add_enabled(free && st.agent, egui::Button::new("Перезапустить")).clicked() {
                        self.run(&ctx, "Перезапускаю агента", |root, s| {
                            stop_agent(root, s);
                            thread::sleep(Duration::from_millis(800));
                            start_agent(root, s)
                        });
                    }
                    if ui.add_enabled(free && st.agent, egui::Button::new("Остановить")).clicked() {
                        self.run(&ctx, "Останавливаю агента", stop_agent);
                    }
                });
                ui.end_row();
            });

            ui.add_space(8.0);
            ui.separator();
            ui.horizontal_wrapped(|ui| {
                ui.label("Режим:");
                ui.radio_value(&mut self.settings.test_mode, false, "обычный");
                ui.radio_value(&mut self.settings.test_mode, true, "испытания полёта");
                ui.add_space(12.0);
                ui.label("Дом x");
                ui.add(egui::TextEdit::singleline(&mut self.settings.home_x).desired_width(56.0));
                ui.label("z");
                ui.add(egui::TextEdit::singleline(&mut self.settings.home_z).desired_width(56.0));
                ui.add_space(12.0);
                ui.checkbox(&mut self.settings.laya_cpu, "Laya на процессоре").on_hover_text(
                    "Вся видеокарта остаётся игре (больше FPS), решения чуть медленнее. Применится при запуске Laya.",
                );
            });
            ui.label(
                RichText::new("Режим и дом применяются при запуске агента: после правки нажми «Перезапустить».")
                    .small()
                    .weak(),
            );
            ui.add_space(6.0);
            ui.horizontal(|ui| {
                if ui.add_enabled(free, egui::Button::new(RichText::new("▶ Запустить всё").strong())).clicked() {
                    self.run(&ctx, "Запускаю всё", |root, s| {
                        let mut out = vec![start_laya(root, s), start_voice(root, s), start_agent(root, s)];
                        out.retain(|l| !l.is_empty());
                        if out.is_empty() { "всё уже работает".into() } else { out.join(" · ") }
                    });
                }
                if ui.add_enabled(free, egui::Button::new("■ Остановить всё")).clicked() {
                    self.run(&ctx, "Останавливаю всё", |root, s| {
                        stop_agent(root, s);
                        stop_voice(root, s);
                        stop_laya(root, s);
                        "агент, голос и Laya остановлены; Minecraft не тронут".into()
                    });
                }
                if ui.button("Папка логов").clicked() {
                    let _ = Command::new("explorer").arg(self.root.join("logs")).spawn();
                }
                if let Some(b) = &busy {
                    ui.spinner();
                    ui.label(format!("{b}…"));
                } else if !note.is_empty() {
                    ui.label(RichText::new(&note).weak());
                }
            });
            ui.add_space(8.0);
        });

        egui::CentralPanel::default().show(ui, |ui| {
            ui.horizontal(|ui| {
                let before = (self.tab, self.only_flight);
                ui.selectable_value(&mut self.tab, LogTab::Agent, "Лог агента");
                ui.selectable_value(&mut self.tab, LogTab::Laya, "Laya");
                ui.selectable_value(&mut self.tab, LogTab::Voice, "Голос");
                ui.add_space(12.0);
                if self.tab == LogTab::Agent {
                    ui.checkbox(&mut self.only_flight, "только полёты");
                }
                if before != (self.tab, self.only_flight) {
                    self.log_read = Instant::now() - Duration::from_secs(10);
                    self.read_log();
                }
            });
            ui.separator();
            egui::ScrollArea::vertical().stick_to_bottom(true).auto_shrink([false, false]).show(ui, |ui| {
                if self.log_lines.is_empty() {
                    ui.label(RichText::new("лог пуст").weak());
                }
                for l in &self.log_lines {
                    let color = if l.contains("level=ERROR") || l.contains("Traceback") {
                        Color32::from_rgb(220, 80, 70)
                    } else if l.contains("level=WARN") {
                        Color32::from_rgb(210, 160, 40)
                    } else {
                        ui.visuals().text_color()
                    };
                    ui.label(RichText::new(l).monospace().size(12.0).color(color));
                }
            });
        });
    }
}

type Job = fn(&Path, &Settings) -> String;

impl Pult {
    #[allow(clippy::too_many_arguments)]
    fn service_buttons(&self, ui: &mut egui::Ui, ctx: &egui::Context, free: bool, up: bool, name: &str, start: Job, stop: Job) {
        ui.horizontal(|ui| {
            if ui.add_enabled(free && !up, egui::Button::new("Запустить")).clicked() {
                self.run(ctx, &format!("Запускаю: {name}"), start);
            }
            if ui.add_enabled(free && up, egui::Button::new("Остановить")).clicked() {
                self.run(ctx, &format!("Останавливаю: {name}"), stop);
            }
        });
    }
}

fn row(ui: &mut egui::Ui, checked: bool, name: &str, up: bool, text: &str) {
    let dot = if !checked {
        Color32::GRAY
    } else if up {
        Color32::from_rgb(70, 180, 90)
    } else {
        Color32::from_rgb(200, 70, 60)
    };
    ui.horizontal(|ui| {
        ui.label(RichText::new("●").color(dot));
        ui.label(RichText::new(name).strong());
    });
    ui.label(text);
}

// ---- services ----

fn start_minecraft() -> String {
    match Command::new(LAUNCHER).current_dir(Path::new(LAUNCHER).parent().unwrap()).spawn() {
        Ok(_) => "лаунчер открыт: нажми «Играть», потом зайди в мир".into(),
        Err(e) => format!("лаунчер не открылся: {e}"),
    }
}

fn log_files(root: &Path, name: &str) -> (Stdio, Stdio) {
    let dir = root.join("logs");
    let _ = fs::create_dir_all(&dir);
    let out = fs::File::create(dir.join(format!("{name}.out.log"))).map(Stdio::from).unwrap_or(Stdio::null());
    let err = fs::File::create(dir.join(format!("{name}.err.log"))).map(Stdio::from).unwrap_or(Stdio::null());
    (out, err)
}

fn start_laya(root: &Path, s: &Settings) -> String {
    if listening(PORT_LAYA) {
        return String::new();
    }
    let (out, err) = log_files(root, "laya");
    let spawned = Command::new(LAYA)
        .envs([
            ("LAYA_HOST", "127.0.0.1"),
            ("LAYA_PORT", "8090"),
            ("LAYA_MODELS", "multilingual"),
            ("LAYA_DEVICE", if s.laya_cpu { "cpu" } else { "cuda" }),
            ("HF_HOME", r"F:\laya\hf"),
            ("HF_HUB_OFFLINE", "1"),
            ("LAYA_LOG_LEVEL", "warning"),
        ])
        .stdin(Stdio::null())
        .stdout(out)
        .stderr(err)
        .creation_flags(NO_WINDOW)
        .spawn();
    if let Err(e) = spawned {
        return format!("Laya не запустилась: {e}");
    }
    if wait_port(PORT_LAYA, 120) { "Laya работает".into() } else { "Laya не поднялась, см. вкладку Laya".into() }
}

fn start_voice(root: &Path, _: &Settings) -> String {
    if listening(PORT_VOICE) {
        return String::new();
    }
    let (out, err) = log_files(root, "voice");
    let spawned = Command::new(root.join(r".venv-voice\Scripts\python.exe"))
        .args(["-u", r"voice\service.py"])
        .current_dir(root)
        .envs([
            ("PYTHONIOENCODING", "utf-8"),
            ("HF_HUB_OFFLINE", "1"),
            ("HF_HOME", r"F:\WinPython64-3.13.11.0dot_post1\whisper_models\hf_cache"),
        ])
        .stdin(Stdio::null())
        .stdout(out)
        .stderr(err)
        .creation_flags(NO_WINDOW)
        .spawn();
    if let Err(e) = spawned {
        return format!("голос не запустился: {e}");
    }
    if wait_port(PORT_VOICE, 90) { "голос работает".into() } else { "голос не поднялся, см. вкладку Голос".into() }
}

fn start_agent(root: &Path, s: &Settings) -> String {
    if agent_running() {
        return String::new();
    }
    let mut cmd = Command::new(BASH);
    cmd.arg(root.join("scripts").join("run-agent.sh").to_string_lossy().replace('\\', "/"))
        .current_dir(root)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .creation_flags(NO_WINDOW);
    if let (Ok(x), Ok(z)) = (s.home_x.trim().parse::<i32>(), s.home_z.trim().parse::<i32>()) {
        cmd.env("HOME_XZ", format!("{x},{z}"));
    }
    if s.test_mode {
        cmd.env("FLIGHT_SHARE", "0.8").env("ACTIVITY_EVERY", "30s");
    }
    if let Err(e) = cmd.spawn() {
        return format!("агент не запустился: {e}");
    }
    for _ in 0..20 {
        thread::sleep(Duration::from_millis(500));
        if agent_running() {
            let mode = if s.test_mode { "испытания полёта" } else { "обычный режим" };
            return format!("агент работает ({mode})");
        }
    }
    "агент не запустился, см. лог агента".into()
}

fn stop_agent(_: &Path, _: &Settings) -> String {
    let _ = Command::new("taskkill").args(["/IM", "pyhtun.exe", "/F"]).creation_flags(NO_WINDOW).output();
    "агент остановлен".into()
}

/// Kill processes whose command line matches; never the PowerShell doing the killing.
fn kill_matching(pattern: &str) {
    let script = format!(
        "Get-CimInstance Win32_Process | Where-Object {{ $_.ProcessId -ne $PID -and $_.CommandLine -match '{pattern}' }} | \
         ForEach-Object {{ Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }}"
    );
    let _ = Command::new("powershell").args(["-NoProfile", "-Command", &script]).creation_flags(NO_WINDOW).output();
}

fn stop_laya(_: &Path, _: &Settings) -> String {
    kill_matching("laya-serve");
    "Laya остановлена".into()
}

fn stop_voice(_: &Path, _: &Settings) -> String {
    kill_matching(r"voice[\\/]service\.py");
    "голос остановлен".into()
}

// ---- status ----

fn poll_status(game: bool) -> Status {
    let modon = listening(PORT_MOD);
    Status {
        game: game || modon,
        modon,
        laya: listening(PORT_LAYA),
        voice: listening(PORT_VOICE),
        agent: agent_running(),
        checked: true,
    }
}

/// The game, not the launcher (both are javaw): the one with a "Minecraft..." window. tasklist,
/// not PowerShell -- a PowerShell start every 1.5 s cost the laptop CPU the game needed.
fn game_running() -> bool {
    Command::new("tasklist")
        .args(["/FI", "IMAGENAME eq javaw.exe", "/FI", "WINDOWTITLE eq Minecraft*", "/NH"])
        .creation_flags(NO_WINDOW)
        .output()
        .map(|o| String::from_utf8_lossy(&o.stdout).to_lowercase().contains("javaw"))
        .unwrap_or(false)
}

fn agent_running() -> bool {
    listening(PORT_AGENT)
}

fn listening(port: u16) -> bool {
    let addr = SocketAddr::from(([127, 0, 0, 1], port));
    TcpStream::connect_timeout(&addr, Duration::from_millis(250)).is_ok()
}

fn wait_port(port: u16, secs: u64) -> bool {
    let until = Instant::now() + Duration::from_secs(secs);
    while Instant::now() < until {
        if listening(port) {
            return true;
        }
        thread::sleep(Duration::from_secs(1));
    }
    false
}

// ---- files ----

/// The persona-agent folder: up from the exe to the one with go.mod, else the usual place.
fn find_root() -> PathBuf {
    if let Ok(exe) = std::env::current_exe() {
        for dir in exe.ancestors().skip(1) {
            if dir.join("go.mod").exists() && dir.join("scripts").exists() {
                return dir.to_path_buf();
            }
        }
    }
    PathBuf::from(r"F:\persona-agent")
}

fn tail(path: &Path, max: u64) -> String {
    let Ok(mut f) = fs::File::open(path) else { return String::new() };
    let len = f.metadata().map(|m| m.len()).unwrap_or(0);
    let start = len.saturating_sub(max);
    if f.seek(SeekFrom::Start(start)).is_err() {
        return String::new();
    }
    let mut buf = Vec::new();
    let _ = f.read_to_end(&mut buf);
    let text = String::from_utf8_lossy(&buf).into_owned();
    if start > 0 {
        // The first line is cut in the middle: drop it.
        return text.split_once('\n').map(|(_, rest)| rest.to_string()).unwrap_or_default();
    }
    text
}

/// `time=2026-10-10T15:56:35.727+03:00 level=INFO msg="flight: done" reason=landed`
/// -> `15:56:35 flight: done  reason=landed`.
fn tidy_line(l: &str) -> String {
    let l = l.trim_end();
    let Some(rest) = l.strip_prefix("time=") else { return l.to_string() };
    let (Some(t), Some(sp)) = (rest.find('T'), rest.find(' ')) else { return l.to_string() };
    let clock = rest.get(t + 1..t + 9).unwrap_or("");
    let mut body = &rest[sp + 1..];
    let mut level = "";
    if let Some(b) = body.strip_prefix("level=INFO ") {
        body = b;
    } else if let Some(i) = body.find(' ') {
        (level, body) = (&body[..i + 1], &body[i + 1..]); // WARN and ERROR stay visible
    }
    let msg = if let Some(q) = body.strip_prefix("msg=\"") {
        match q.find('"') {
            Some(end) => format!("{}  {}", &q[..end], q[end + 1..].trim_start()),
            None => q.to_string(),
        }
    } else {
        body.strip_prefix("msg=").unwrap_or(body).to_string()
    };
    format!("{clock} {level}{}", msg.trim_end())
}

fn settings_path(root: &Path) -> PathBuf {
    root.join("data").join("pult.ini")
}

fn load_settings(root: &Path) -> Settings {
    let mut s = Settings::default();
    let Ok(text) = fs::read_to_string(settings_path(root)) else { return s };
    for line in text.lines() {
        match line.split_once('=') {
            Some(("test_mode", v)) => s.test_mode = v == "1",
            Some(("home_x", v)) => s.home_x = v.to_string(),
            Some(("home_z", v)) => s.home_z = v.to_string(),
            Some(("laya_cpu", v)) => s.laya_cpu = v == "1",
            _ => {}
        }
    }
    s
}

fn save_settings(root: &Path, s: &Settings) {
    let b = |v: bool| if v { "1" } else { "0" };
    let text = format!(
        "test_mode={}\nhome_x={}\nhome_z={}\nlaya_cpu={}\n",
        b(s.test_mode),
        s.home_x.trim(),
        s.home_z.trim(),
        b(s.laya_cpu)
    );
    let _ = fs::create_dir_all(root.join("data"));
    let _ = fs::write(settings_path(root), text);
}

/// Segoe UI and Consolas from Windows, ahead of the bundled egui fonts.
fn setup_fonts(ctx: &egui::Context) {
    let mut fonts = egui::FontDefinitions::default();
    for (name, file, family) in [
        ("segoe", r"C:\Windows\Fonts\segoeui.ttf", egui::FontFamily::Proportional),
        ("consolas", r"C:\Windows\Fonts\consola.ttf", egui::FontFamily::Monospace),
    ] {
        if let Ok(bytes) = fs::read(file) {
            fonts.font_data.insert(name.into(), Arc::new(egui::FontData::from_owned(bytes)));
            fonts.families.entry(family).or_default().insert(0, name.into());
        }
    }
    ctx.set_fonts(fonts);
}

#[cfg(test)]
mod tests {
    use super::tidy_line;

    #[test]
    fn tidies_slog_lines() {
        assert_eq!(
            tidy_line(r#"time=2026-10-10T15:56:35.727+03:00 level=INFO msg="flight: done" reason=landed"#),
            "15:56:35 flight: done  reason=landed"
        );
        assert_eq!(
            tidy_line(r#"time=2026-10-10T04:24:05.1+03:00 level=WARN msg="game connection lost, waiting" err=x"#),
            "04:24:05 level=WARN game connection lost, waiting  err=x"
        );
        assert_eq!(tidy_line("time=2026-10-10T04:23:39.0+03:00 level=INFO msg=ready"), "04:23:39 ready");
        assert_eq!(tidy_line("Traceback (most recent call last):"), "Traceback (most recent call last):");
    }
}
