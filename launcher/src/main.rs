// Пульт Пыхтуна: start, stop and watch everything the character needs (Minecraft, Laya, the
// voice service, the agent) without a terminal. Services keep running when the window closes.
#![windows_subsystem = "windows"]

use std::fs;
use std::io::{Read, Seek, SeekFrom, Write};
use std::net::{SocketAddr, TcpStream};
use std::os::windows::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

use eframe::egui::{self, Color32, RichText};

const NO_WINDOW: u32 = 0x0800_0000; // CREATE_NO_WINDOW: no console flashes for services
const BASH: &str = r"C:\Program Files\Git\bin\bash.exe";
const PORT_MOD: u16 = 25350;
const PORT_LAYA: u16 = 8090;
const PORT_VOICE: u16 = 8091;
const PORT_AGENT: u16 = 8092; // the agent's /voice and /status: up while the agent runs

/// The interface language: Russian unless lang=en in data/pult.ini.
static EN: AtomicBool = AtomicBool::new(false);

fn tr(ru: &'static str, en: &'static str) -> &'static str {
    if EN.load(Ordering::Relaxed) { en } else { ru }
}

fn main() -> eframe::Result {
    EN.store(load_settings(&find_root()).lang == "en", Ordering::Relaxed);
    let options = eframe::NativeOptions {
        viewport: egui::ViewportBuilder::default()
            .with_title(tr("Пыхтун — пульт", "Pyhtun — control panel"))
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
    flight: Option<Flight>, // from the agent's /status; None from an agent build without it
    checked: bool,
}

/// The flight under way, else the last one, as the agent reports it.
#[derive(Clone, Default, Debug, PartialEq)]
struct Flight {
    flying: bool,
    style: String,
    phase: String,
    reason: String,
    mode: String,
    speed: f64,
    distance: f64,
    rockets: i64,
    seconds: i64,
    solver: bool,
    flights: i64,
}

/// Settings kept between runs in data/pult.ini.
#[derive(Clone)]
struct Settings {
    test_mode: bool, // flight testing: long legs out from home and back
    home_x: String,
    home_z: String,
    laya_cpu: bool, // Laya on the CPU leaves the whole GPU to the game
    flight_style: String, // "" mixes them; normal, low, tricks, route fix one
    twitch: String,       // the stream's channel: the agent reads its chat (empty: off)
    solver: bool,         // the fork's physics solver steers long legs, rings and pursuit
    lang: String,         // "ru" or "en"
    record_tests: bool,   // film the bench courses from the bot's client
    // Where things live on this machine; set them in data/pult.ini.
    mc_launcher: String,     // the Minecraft launcher's exe
    laya_serve: String,      // laya-serve.exe
    laya_hf_home: String,    // HF_HOME with Laya's weights (empty: the default cache)
    whisper_hf_home: String, // HF_HOME with the Whisper model (empty: the default cache)
    unionclef_dir: String,   // the UnionClef fork's folder: the bench courses run from it
    test_python: String,     // the python that runs them (empty: python on PATH)
    test_env: String,        // more env for them: K=V;K=V (rcon, py4j python, nicks)
}

impl Default for Settings {
    fn default() -> Self {
        Settings {
            test_mode: false,
            home_x: "-471".into(),
            home_z: "-183".into(),
            laya_cpu: false,
            flight_style: String::new(),
            twitch: String::new(),
            solver: true,
            lang: "ru".into(),
            record_tests: false,
            mc_launcher: String::new(),
            laya_serve: String::new(),
            laya_hf_home: String::new(),
            whisper_hf_home: String::new(),
            unionclef_dir: String::new(),
            test_python: String::new(),
            test_env: String::new(),
        }
    }
}

#[derive(Clone, Copy, PartialEq)]
enum LogTab {
    Agent,
    Laya,
    Voice,
    Tests,
}

struct Shared {
    status: Status,
    busy: Option<String>, // a start or stop under way
    note: String,         // the last outcome, shown under the buttons
    course: Option<String>, // a bench course under way (it runs beside starts and stops)
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
        let shared = Arc::new(Mutex::new(Shared { status: Status::default(), busy: None, note: String::new(), course: None }));
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

    /// Run bench courses (comma-separated uctest ids) in the background; their output goes to the
    /// Courses log tab and the score line to the note.
    fn run_courses(&mut self, ctx: &egui::Context, which: &'static str, name: &'static str) {
        let mut sh = self.shared.lock().unwrap();
        if sh.course.is_some() {
            return;
        }
        sh.course = Some(name.to_string());
        drop(sh);
        save_settings(&self.root, &self.settings);
        self.tab = LogTab::Tests;
        self.log_read = Instant::now() - Duration::from_secs(10);
        let (shared, root, settings, ctx) = (self.shared.clone(), self.root.clone(), self.settings.clone(), ctx.clone());
        thread::spawn(move || {
            let note = run_courses(&root, &settings, which);
            let mut sh = shared.lock().unwrap();
            sh.course = None;
            sh.note = note;
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
            LogTab::Tests => "flight-tests.log",
        };
        let text = tail(&self.root.join("logs").join(file), 96 * 1024);
        let mut lines: Vec<String> = text
            .lines()
            .map(tidy_line)
            .filter(|l| self.tab != LogTab::Agent || !self.only_flight || l.contains("flight") || l.contains("fly") || l.contains("hands"))
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
        let (st, busy, note, course) = {
            let sh = self.shared.lock().unwrap();
            (sh.status.clone(), sh.busy.clone(), sh.note.clone(), sh.course.clone())
        };
        let free = busy.is_none();

        egui::Panel::top("controls").show(ui, |ui| {
            ui.add_space(8.0);
            ui.heading(tr("Пыхтун", "Pyhtun"));
            ui.add_space(4.0);
            egui::Grid::new("status").num_columns(3).spacing([16.0, 6.0]).show(ui, |ui| {
                let (up, text) = if st.modon {
                    (true, tr("в игре, мод на связи", "in game, the mod answers"))
                } else if st.game {
                    (false, tr("запущен, мод не отвечает — зайди в мир", "running, the mod does not answer: join a world"))
                } else {
                    (false, tr("не запущен", "not running"))
                };
                row(ui, st.checked, "Minecraft", up, text);
                if ui.add_enabled(!st.game && free, egui::Button::new(tr("Открыть лаунчер", "Open the launcher"))).clicked() {
                    self.run(&ctx, tr("Открываю лаунчер", "Opening the launcher"), |_, s| start_minecraft(s));
                }
                ui.end_row();

                row(ui, st.checked, tr("Laya (решения)", "Laya (decisions)"), st.laya, if st.laya { tr("работает", "running") } else { tr("выключена", "off") });
                self.service_buttons(ui, &ctx, free, st.laya, "Laya", start_laya, stop_laya);
                ui.end_row();

                row(ui, st.checked, tr("Голос", "Voice"), st.voice, if st.voice { tr("работает", "running") } else { tr("выключен", "off") });
                self.service_buttons(ui, &ctx, free, st.voice, tr("голос", "voice"), start_voice, stop_voice);
                ui.end_row();

                row(ui, st.checked, tr("Агент", "Agent"), st.agent, if st.agent { tr("работает", "running") } else { tr("выключен", "off") });
                ui.horizontal(|ui| {
                    if ui.add_enabled(free && !st.agent, egui::Button::new(tr("Запустить", "Start"))).clicked() {
                        self.run(&ctx, tr("Запускаю агента", "Starting the agent"), start_agent);
                    }
                    if ui.add_enabled(free && st.agent, egui::Button::new(tr("Перезапустить", "Restart"))).clicked() {
                        self.run(&ctx, tr("Перезапускаю агента", "Restarting the agent"), |root, s| {
                            stop_agent(root, s);
                            thread::sleep(Duration::from_millis(800));
                            start_agent(root, s)
                        });
                    }
                    if ui.add_enabled(free && st.agent, egui::Button::new(tr("Остановить", "Stop"))).clicked() {
                        self.run(&ctx, tr("Останавливаю агента", "Stopping the agent"), stop_agent);
                    }
                });
                ui.end_row();

                let flying = st.flight.as_ref().is_some_and(|f| f.flying);
                row(ui, st.checked && st.agent, tr("Полёт", "Flight"), flying, &flight_text(st.agent, st.flight.as_ref()));
                ui.label("");
                ui.end_row();
            });

            ui.add_space(8.0);
            ui.separator();
            ui.horizontal_wrapped(|ui| {
                ui.label(tr("Режим:", "Mode:"));
                ui.radio_value(&mut self.settings.test_mode, false, tr("обычный", "normal"));
                ui.radio_value(&mut self.settings.test_mode, true, tr("испытания полёта", "flight testing"));
                ui.add_space(12.0);
                ui.label(tr("Дом x", "Home x"));
                ui.add(egui::TextEdit::singleline(&mut self.settings.home_x).desired_width(56.0));
                ui.label("z");
                ui.add(egui::TextEdit::singleline(&mut self.settings.home_z).desired_width(56.0));
                ui.add_space(12.0);
                let styles = [
                    ("", tr("смешанные", "mixed")),
                    ("normal", tr("обычные", "normal")),
                    ("low", tr("бреющий", "low")),
                    ("tricks", tr("пилотаж", "aerobatics")),
                    ("route", tr("облёт", "route")),
                ];
                let shown = styles.iter().find(|(k, _)| *k == self.settings.flight_style).map(|(_, v)| *v).unwrap_or(styles[0].1);
                ui.label(tr("Полёты:", "Flights:"));
                egui::ComboBox::from_id_salt("style").selected_text(shown).show_ui(ui, |ui| {
                    for (k, v) in styles {
                        ui.selectable_value(&mut self.settings.flight_style, k.to_string(), v);
                    }
                });
                ui.checkbox(&mut self.settings.solver, tr("Физика", "Physics")).on_hover_text(tr(
                    "Решатель на физике игры ведёт длинные перелёты, кольца и погоню; выключено — всё летит обычный пилот. Применится при запуске агента.",
                    "A solver on the game's physics steers long legs, rings and pursuit; off, the plain pilot flies all of it. Applies when the agent starts.",
                ));
                ui.add_space(12.0);
                ui.label("Twitch").on_hover_text(tr(
                    "Имя канала стрима: агент читает его чат и отвечает зрителям. Пусто — выключено. Применится при запуске агента.",
                    "The stream's channel: the agent reads its chat and answers viewers. Empty: off. Applies when the agent starts.",
                ));
                ui.add(egui::TextEdit::singleline(&mut self.settings.twitch).desired_width(90.0).hint_text(tr("канал", "channel")));
                ui.add_space(12.0);
                ui.checkbox(&mut self.settings.laya_cpu, tr("Laya на процессоре", "Laya on the CPU")).on_hover_text(tr(
                    "Вся видеокарта остаётся игре (больше FPS), решения чуть медленнее. Применится при запуске Laya.",
                    "The whole GPU stays with the game (more FPS), decisions a little slower. Applies when Laya starts.",
                ));
                ui.add_space(12.0);
                let before = self.settings.lang.clone();
                egui::ComboBox::from_id_salt("lang").selected_text(if self.settings.lang == "en" { "EN" } else { "RU" }).show_ui(ui, |ui| {
                    ui.selectable_value(&mut self.settings.lang, "ru".to_string(), "RU");
                    ui.selectable_value(&mut self.settings.lang, "en".to_string(), "EN");
                });
                if before != self.settings.lang {
                    EN.store(self.settings.lang == "en", Ordering::Relaxed);
                    save_settings(&self.root, &self.settings);
                    ctx.send_viewport_cmd(egui::ViewportCommand::Title(tr("Пыхтун — пульт", "Pyhtun — control panel").into()));
                }
            });
            ui.label(
                RichText::new(tr(
                    "Режим, дом и физика применяются при запуске агента: после правки нажми «Перезапустить».",
                    "Mode, home and physics apply when the agent starts: after a change press Restart.",
                ))
                    .small()
                    .weak(),
            );
            ui.add_space(6.0);
            ui.horizontal(|ui| {
                if ui.add_enabled(free, egui::Button::new(RichText::new(tr("▶ Запустить всё", "▶ Start all")).strong())).clicked() {
                    self.run(&ctx, tr("Запускаю всё", "Starting everything"), |root, s| {
                        let mut out = vec![start_laya(root, s), start_voice(root, s), start_agent(root, s)];
                        out.retain(|l| !l.is_empty());
                        if out.is_empty() { tr("всё уже работает", "all running already").into() } else { out.join(" · ") }
                    });
                }
                if ui.add_enabled(free, egui::Button::new(tr("■ Остановить всё", "■ Stop all"))).clicked() {
                    self.run(&ctx, tr("Останавливаю всё", "Stopping everything"), |root, s| {
                        stop_agent(root, s);
                        stop_voice(root, s);
                        stop_laya(root, s);
                        tr("агент, голос и Laya остановлены; Minecraft не тронут", "agent, voice and Laya stopped; Minecraft left as it is").into()
                    });
                }
                if ui
                    .button(tr("Карта", "Map"))
                    .on_hover_text(tr("Что Пыхтун разведал: земля, находки, полёты (data/map.html)", "What Pyhtun has scouted: ground, finds, flights (data/map.html)"))
                    .clicked()
                {
                    let _ = Command::new("explorer").arg(self.root.join("data").join("map.html")).spawn();
                }
                if ui.button(tr("Папка логов", "Logs folder")).clicked() {
                    let _ = Command::new("explorer").arg(self.root.join("logs")).spawn();
                }
                if let Some(b) = &busy {
                    ui.spinner();
                    ui.label(format!("{b}…"));
                } else if !note.is_empty() {
                    ui.label(RichText::new(&note).weak());
                }
            });
            ui.horizontal_wrapped(|ui| {
                let ready = !self.settings.unionclef_dir.trim().is_empty();
                let why = if ready {
                    tr(
                        "Курсы uctest на локальном плоском сервере: нужен запущенный сервер и клиент бота на нём, для следования ещё клиент ведущего",
                        "The uctest courses on a local flat server: needs the server up and the bot's client on it, and the leader's client for Follow",
                    )
                } else {
                    tr("Задай unionclef_dir (папка форка UnionClef) в data/pult.ini", "Set unionclef_dir (the UnionClef fork's folder) in data/pult.ini")
                };
                ui.label(tr("Испытания на стенде:", "Bench courses:")).on_hover_text(why);
                let can = ready && course.is_none();
                for (ids, ru, en) in [
                    ("elytra_rings", "Кольца", "Rings"),
                    ("elytra_500", "500 блоков", "500 blocks"),
                    ("elytra_follow", "Следование", "Follow"),
                    ("elytra_rings,elytra_500,elytra_follow", "Все три", "All three"),
                ] {
                    if ui.add_enabled(can, egui::Button::new(tr(ru, en))).on_disabled_hover_text(why).clicked() {
                        self.run_courses(&ctx, ids, tr(ru, en));
                    }
                }
                ui.checkbox(&mut self.settings.record_tests, tr("снимать", "film"))
                    .on_hover_text(tr("Видео каждого курса с клиента бота", "A video of each course from the bot's client"));
                if let Some(c) = &course {
                    ui.spinner();
                    ui.label(format!("{}: {c}…", tr("идёт", "running")));
                }
            });
            ui.add_space(8.0);
        });

        egui::CentralPanel::default().show(ui, |ui| {
            ui.horizontal(|ui| {
                let before = (self.tab, self.only_flight);
                ui.selectable_value(&mut self.tab, LogTab::Agent, tr("Лог агента", "Agent log"));
                ui.selectable_value(&mut self.tab, LogTab::Laya, "Laya");
                ui.selectable_value(&mut self.tab, LogTab::Voice, tr("Голос", "Voice"));
                ui.selectable_value(&mut self.tab, LogTab::Tests, tr("Испытания", "Courses"));
                ui.add_space(12.0);
                if self.tab == LogTab::Agent {
                    ui.checkbox(&mut self.only_flight, tr("только полёты", "flights only"));
                }
                if before != (self.tab, self.only_flight) {
                    self.log_read = Instant::now() - Duration::from_secs(10);
                    self.read_log();
                }
            });
            ui.separator();
            egui::ScrollArea::vertical().stick_to_bottom(true).auto_shrink([false, false]).show(ui, |ui| {
                if self.log_lines.is_empty() {
                    ui.label(RichText::new(tr("лог пуст", "the log is empty")).weak());
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
            if ui.add_enabled(free && !up, egui::Button::new(tr("Запустить", "Start"))).clicked() {
                self.run(ctx, &format!("{}: {name}", tr("Запускаю", "Starting")), start);
            }
            if ui.add_enabled(free && up, egui::Button::new(tr("Остановить", "Stop"))).clicked() {
                self.run(ctx, &format!("{}: {name}", tr("Останавливаю", "Stopping")), stop);
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

fn start_minecraft(s: &Settings) -> String {
    if s.mc_launcher.is_empty() {
        return tr("лаунчер не задан: mc_launcher=<путь к exe> в data/pult.ini", "no launcher set: mc_launcher=<path to the exe> in data/pult.ini").into();
    }
    let exe = Path::new(&s.mc_launcher);
    match Command::new(exe).current_dir(exe.parent().unwrap_or(Path::new("."))).spawn() {
        Ok(_) => tr("лаунчер открыт: нажми «Играть», потом зайди в мир", "the launcher is open: press Play, then join a world").into(),
        Err(e) => format!("{}: {e}", tr("лаунчер не открылся", "the launcher did not open")),
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
    if s.laya_serve.is_empty() {
        return tr("Laya не задана: laya_serve=<путь к laya-serve.exe> в data/pult.ini", "no Laya set: laya_serve=<path to laya-serve.exe> in data/pult.ini").into();
    }
    let (out, err) = log_files(root, "laya");
    let mut cmd = Command::new(&s.laya_serve);
    if !s.laya_hf_home.is_empty() {
        cmd.env("HF_HOME", &s.laya_hf_home);
    }
    let spawned = cmd
        .envs([
            ("LAYA_HOST", "127.0.0.1"),
            ("LAYA_PORT", "8090"),
            ("LAYA_MODELS", "multilingual"),
            ("LAYA_DEVICE", if s.laya_cpu { "cpu" } else { "cuda" }),
            ("HF_HUB_OFFLINE", "1"),
            ("LAYA_LOG_LEVEL", "warning"),
        ])
        .stdin(Stdio::null())
        .stdout(out)
        .stderr(err)
        .creation_flags(NO_WINDOW)
        .spawn();
    if let Err(e) = spawned {
        return format!("{}: {e}", tr("Laya не запустилась", "Laya did not start"));
    }
    if wait_port(PORT_LAYA, 120) {
        tr("Laya работает", "Laya is running").into()
    } else {
        tr("Laya не поднялась, см. вкладку Laya", "Laya did not come up, see the Laya tab").into()
    }
}

fn start_voice(root: &Path, s: &Settings) -> String {
    if listening(PORT_VOICE) {
        return String::new();
    }
    let (out, err) = log_files(root, "voice");
    let mut cmd = Command::new(root.join(r".venv-voice\Scripts\python.exe"));
    if !s.whisper_hf_home.is_empty() {
        cmd.env("HF_HOME", &s.whisper_hf_home);
    }
    let spawned = cmd
        .args(["-u", r"voice\service.py"])
        .current_dir(root)
        .envs([("PYTHONIOENCODING", "utf-8"), ("HF_HUB_OFFLINE", "1")])
        .stdin(Stdio::null())
        .stdout(out)
        .stderr(err)
        .creation_flags(NO_WINDOW)
        .spawn();
    if let Err(e) = spawned {
        return format!("{}: {e}", tr("голос не запустился", "the voice did not start"));
    }
    if wait_port(PORT_VOICE, 90) {
        tr("голос работает", "the voice is running").into()
    } else {
        tr("голос не поднялся, см. вкладку Голос", "the voice did not come up, see the Voice tab").into()
    }
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
    if !s.flight_style.is_empty() {
        cmd.env("FLIGHT_STYLE", &s.flight_style);
    }
    if !s.twitch.trim().is_empty() {
        cmd.env("TWITCH_CHANNEL", s.twitch.trim());
    }
    cmd.env("FLY_SOLVER", if s.solver { "1" } else { "0" });
    if let Err(e) = cmd.spawn() {
        return format!("{}: {e}", tr("агент не запустился", "the agent did not start"));
    }
    for _ in 0..20 {
        thread::sleep(Duration::from_millis(500));
        if agent_running() {
            let mode = if s.test_mode { tr("испытания полёта", "flight testing") } else { tr("обычный режим", "normal mode") };
            let physics = if s.solver { tr("на физике", "on physics") } else { tr("обычный пилот", "plain pilot") };
            return format!("{} ({mode}, {physics})", tr("агент работает", "the agent is running"));
        }
    }
    tr("агент не запустился, см. лог агента", "the agent did not start, see the agent log").into()
}

fn stop_agent(_: &Path, _: &Settings) -> String {
    let _ = Command::new("taskkill").args(["/IM", "pyhtun.exe", "/F"]).creation_flags(NO_WINDOW).output();
    tr("агент остановлен", "the agent is stopped").into()
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
    tr("Laya остановлена", "Laya is stopped").into()
}

fn stop_voice(_: &Path, _: &Settings) -> String {
    kill_matching(r"voice[\\/]service\.py");
    tr("голос остановлен", "the voice is stopped").into()
}

/// The bench courses: deploy/runner/elytra_local.py in the fork, output to logs/flight-tests.log.
/// The note is its last line, the score per course ({"elytra_rings": "1/1", ...}).
fn run_courses(root: &Path, s: &Settings, which: &str) -> String {
    let dir = Path::new(s.unionclef_dir.trim()).join("deploy").join("runner");
    if !dir.join("elytra_local.py").exists() {
        return format!("{} {}", tr("нет elytra_local.py в", "no elytra_local.py in"), dir.display());
    }
    let logs = root.join("logs");
    let _ = fs::create_dir_all(&logs);
    let log = logs.join("flight-tests.log");
    let (Ok(out), Ok(err)) = (fs::File::create(&log), fs::File::options().append(true).open(&log)) else {
        return tr("не открылся logs/flight-tests.log", "could not open logs/flight-tests.log").into();
    };
    let python = if s.test_python.trim().is_empty() { "python" } else { s.test_python.trim() };
    let mut cmd = Command::new(python);
    cmd.args(["-u", "elytra_local.py", which])
        .current_dir(&dir)
        .env("PYTHONIOENCODING", "utf-8")
        .stdin(Stdio::null())
        .stdout(out)
        .stderr(err)
        .creation_flags(NO_WINDOW);
    if s.record_tests {
        cmd.arg("--record");
    }
    for kv in s.test_env.split(';') {
        if let Some((k, v)) = kv.split_once('=') {
            cmd.env(k.trim(), v.trim());
        }
    }
    match cmd.status() {
        Err(e) => format!("{}: {e}", tr("испытания не запустились", "the courses did not start")),
        Ok(code) => {
            let text = tail(&log, 4096);
            match text.lines().rev().find(|l| l.trim_start().starts_with('{')) {
                Some(score) => format!("{}: {}", tr("испытания", "courses"), score.trim()),
                None => format!("{} ({code}), {}", tr("испытания прервались", "the courses stopped"), tr("см. вкладку Испытания", "see the Courses tab")),
            }
        }
    }
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
        flight: agent_status().map(|s| parse_flight(&s)),
        checked: true,
    }
}

/// GET /status from the agent; None when it is down or a build without it.
fn agent_status() -> Option<String> {
    let addr = SocketAddr::from(([127, 0, 0, 1], PORT_AGENT));
    let mut conn = TcpStream::connect_timeout(&addr, Duration::from_millis(250)).ok()?;
    conn.set_read_timeout(Some(Duration::from_millis(500))).ok()?;
    conn.write_all(b"GET /status HTTP/1.0\r\nHost: 127.0.0.1\r\n\r\n").ok()?;
    let mut reply = String::new();
    conn.read_to_string(&mut reply).ok()?;
    let (head, body) = reply.split_once("\r\n\r\n")?;
    head.lines().next()?.contains(" 200 ").then(|| body.to_string())
}

/// One value of a flat JSON object, unquoted: enough for the agent's status, no parser needed.
fn json_value<'a>(json: &'a str, key: &str) -> Option<&'a str> {
    let k = format!("\"{key}\":");
    let rest = &json[json.find(&k)? + k.len()..];
    if let Some(q) = rest.strip_prefix('"') {
        return q.find('"').map(|end| &q[..end]);
    }
    let end = rest.find([',', '}']).unwrap_or(rest.len());
    Some(rest[..end].trim())
}

fn parse_flight(json: &str) -> Flight {
    let text = |k| json_value(json, k).unwrap_or("").to_string();
    let num = |k| json_value(json, k).and_then(|v| v.parse::<f64>().ok()).unwrap_or(0.0);
    Flight {
        flying: json_value(json, "flying") == Some("true"),
        style: text("style"),
        phase: text("phase"),
        reason: text("reason"),
        mode: text("mode"),
        speed: num("speed"),
        distance: num("distance"),
        rockets: num("rockets") as i64,
        seconds: num("seconds") as i64,
        solver: json_value(json, "solver") == Some("true"),
        flights: num("flights") as i64,
    }
}

fn style_name(style: &str) -> &'static str {
    match style {
        "normal" => tr("обычный", "normal"),
        "low" => tr("бреющий", "low"),
        "tricks" => tr("пилотаж", "aerobatics"),
        "route" => tr("облёт", "route"),
        "wingman" => tr("ведомый", "wingman"),
        _ => "",
    }
}

/// The Flight row: the one under way, else the last.
fn flight_text(agent: bool, f: Option<&Flight>) -> String {
    let Some(f) = f.filter(|_| agent) else {
        return if agent { tr("агент не сообщает полёт (старая сборка)", "the agent does not report flights (an old build)") } else { tr("агент выключен", "the agent is off") }.into();
    };
    let physics = if f.solver { tr("на физике", "on physics") } else { tr("обычный пилот", "plain pilot") };
    if f.flying {
        let mode = if f.mode.is_empty() { String::new() } else { format!(", {}", f.mode) };
        return format!(
            "{} {}{mode}, {physics} · {:.2} {} · {} {:.0} · {} {} · {} {}",
            tr("летит:", "flying:"),
            style_name(&f.style),
            f.speed,
            tr("бл/тик", "bl/tick"),
            tr("до цели", "to go"),
            f.distance,
            tr("ракет", "rockets"),
            f.rockets,
            f.seconds,
            tr("с", "s"),
        );
    }
    if f.flights == 0 {
        return format!("{} · {physics}", tr("ещё не летал", "no flights yet"));
    }
    let why = if f.reason.is_empty() { String::new() } else { format!(" ({})", f.reason) };
    format!(
        "{} {} {}{why} · {} {} · {} {} · {} {}",
        tr("последний:", "last:"),
        style_name(&f.style),
        f.phase,
        tr("ракет", "rockets"),
        f.rockets,
        f.seconds,
        tr("с", "s"),
        tr("всего", "in all"),
        f.flights,
    )
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

/// The persona-agent folder: up from the exe to the one with go.mod, else the current folder.
fn find_root() -> PathBuf {
    if let Ok(exe) = std::env::current_exe() {
        for dir in exe.ancestors().skip(1) {
            if dir.join("go.mod").exists() && dir.join("scripts").exists() {
                return dir.to_path_buf();
            }
        }
    }
    PathBuf::from(".")
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
            Some(("flight_style", v)) => s.flight_style = v.to_string(),
            Some(("twitch", v)) => s.twitch = v.to_string(),
            Some(("solver", v)) => s.solver = v != "0",
            Some(("lang", v)) => s.lang = v.to_string(),
            Some(("record_tests", v)) => s.record_tests = v == "1",
            Some(("unionclef_dir", v)) => s.unionclef_dir = v.to_string(),
            Some(("test_python", v)) => s.test_python = v.to_string(),
            Some(("test_env", v)) => s.test_env = v.to_string(),
            Some(("mc_launcher", v)) => s.mc_launcher = v.to_string(),
            Some(("laya_serve", v)) => s.laya_serve = v.to_string(),
            Some(("laya_hf_home", v)) => s.laya_hf_home = v.to_string(),
            Some(("whisper_hf_home", v)) => s.whisper_hf_home = v.to_string(),
            _ => {}
        }
    }
    s
}

fn save_settings(root: &Path, s: &Settings) {
    let b = |v: bool| if v { "1" } else { "0" };
    let text = format!(
        "test_mode={}\nhome_x={}\nhome_z={}\nlaya_cpu={}\nflight_style={}\ntwitch={}\nsolver={}\nlang={}\n\
         record_tests={}\nmc_launcher={}\nlaya_serve={}\nlaya_hf_home={}\nwhisper_hf_home={}\n\
         unionclef_dir={}\ntest_python={}\ntest_env={}\n",
        b(s.test_mode),
        s.home_x.trim(),
        s.home_z.trim(),
        b(s.laya_cpu),
        s.flight_style,
        s.twitch.trim(),
        b(s.solver),
        s.lang,
        b(s.record_tests),
        s.mc_launcher,
        s.laya_serve,
        s.laya_hf_home,
        s.whisper_hf_home,
        s.unionclef_dir.trim(),
        s.test_python.trim(),
        s.test_env.trim()
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
    use super::{parse_flight, tidy_line, Flight};

    #[test]
    fn reads_the_agents_flight() {
        let json = r#"{"flight":{"flying":true,"style":"low","phase":"flying","mode":"cruise","speed":1.42,"distance":87.5,"rockets":3,"seconds":12,"solver":true,"flights":4}}"#;
        assert_eq!(
            parse_flight(json),
            Flight {
                flying: true,
                style: "low".into(),
                phase: "flying".into(),
                reason: String::new(),
                mode: "cruise".into(),
                speed: 1.42,
                distance: 87.5,
                rockets: 3,
                seconds: 12,
                solver: true,
                flights: 4,
            }
        );
        assert_eq!(parse_flight(r#"{"flight":{"flying":false,"speed":0,"distance":0,"rockets":0,"seconds":0,"solver":false,"flights":0}}"#).flying, false);
    }

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
