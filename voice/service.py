"""Voice for the character: hears players in the game's voice chat, speaks through a virtual cable.

Hearing: UnionClef sends every Simple Voice Chat packet as a UDP datagram to 127.0.0.1:25360, already
tagged with the player's name, so speakers are separated for free (docs/voice-transport.md: why not
Py4J). A phrase ends when a player's packets stop arriving; it is transcribed and posted to the Go
agent.

Speaking: the agent POSTs /say; Piper synthesizes and plays into "CABLE Input", which the game
uses as its microphone.

Avatar: GET /avatar is the character's face for an OBS Browser Source; it polls /avatar/state for
the loudness of the voice being played (lip sync) and the mood the agent POSTs to /mood.
"""

import argparse
import concurrent.futures
import json
import logging
import os
import queue
import re
import socket
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import numpy as np
import sounddevice as sd
import soxr
from faster_whisper import WhisperModel
from piper import PiperVoice

log = logging.getLogger("voice")

FEED_RATE = 48000  # Simple Voice Chat: 16-bit mono PCM at 48 kHz
STT_RATE = 16000
PHRASE_GAP = 0.6  # seconds without packets that end a phrase
MIN_PHRASE = 0.4  # shorter bursts are noise or clicks
MAX_PHRASE = 15.0

# Whisper fills silence and noise with these; drop them.
HALLUCINATIONS = ("продолжение следует", "субтитры", "спасибо за просмотр", "редактор субтитров")


class Hearing:
    """Collects voice packets per player and emits whole phrases."""

    def __init__(self, phrases: "queue.Queue[tuple[str, np.ndarray]]"):
        self.phrases = phrases
        self.lock = threading.Lock()
        self.buffers: dict[str, list[bytes]] = {}
        self.last: dict[str, float] = {}
        threading.Thread(target=self._flusher, daemon=True).start()

    def feed(self, nick: str, pcm: bytes) -> None:
        with self.lock:
            self.buffers.setdefault(nick, []).append(pcm)
            self.last[nick] = time.monotonic()

    def _flusher(self) -> None:
        while True:
            time.sleep(0.1)
            now = time.monotonic()
            ready = []
            with self.lock:
                for nick, t in list(self.last.items()):
                    size = sum(len(b) for b in self.buffers[nick]) / 2 / FEED_RATE
                    if now - t > PHRASE_GAP or size > MAX_PHRASE:
                        ready.append((nick, b"".join(self.buffers.pop(nick))))
                        del self.last[nick]
            for nick, pcm in ready:
                audio = np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768.0
                if len(audio) / FEED_RATE < MIN_PHRASE:
                    continue
                self.phrases.put((nick, soxr.resample(audio, FEED_RATE, STT_RATE)))


def listen_game(hearing: Hearing, nick: str, port: int) -> None:
    """Voice packets from the mod, one UDP datagram each: b"PYV1", u8 length + the nick of the
    player whose game sent it, u8 length + the speaker's nick, PCM 16-bit LE 48 kHz mono. Nothing
    to connect or keep alive: the game starts, restarts or reloads its settings and the next packet
    simply arrives. Another bot's game on this machine sends to the same port; its packets are
    dropped by the first nick."""
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 1 << 20)  # a second of several speakers
    sock.bind(("127.0.0.1", port))
    log.info("hearing the game of %s on udp %d", nick or "any player", port)
    want = nick.lower()
    games: set[str] = set()
    while True:
        try:
            data = sock.recv(65535)
        except OSError:
            continue
        if data[:4] != b"PYV1" or len(data) < 6:
            continue
        try:
            n = data[4]
            own = data[5:5 + n].decode("utf-8", "replace")
            i = 5 + n
            m = data[i]
            speaker = data[i + 1:i + 1 + m].decode("utf-8", "replace")
            pcm = data[i + 1 + m:]
        except IndexError:
            continue
        if want and own.lower() != want:
            continue
        if own not in games:
            games.add(own)
            log.info("voice from the game of %s", own)
        hearing.feed(speaker, pcm)


def transcriber(phrases, model: WhisperModel, agent_url: str, hotwords: str):
    while True:
        nick, audio = phrases.get()
        start = time.perf_counter()
        segments, _ = model.transcribe(
            audio,
            language="ru",
            beam_size=1,
            vad_filter=True,
            condition_on_previous_text=False,
            hotwords=hotwords,
        )
        text = " ".join(s.text.strip() for s in segments).strip()
        ms = int((time.perf_counter() - start) * 1000)
        if not text or any(h in text.lower() for h in HALLUCINATIONS):
            log.info("heard nothing from %s (%.1fs audio, %d ms)", nick, len(audio) / STT_RATE, ms)
            continue
        log.info("heard %s: %s (%.1fs audio, stt %d ms)", nick, text, len(audio) / STT_RATE, ms)
        body = json.dumps({"nick": nick, "text": text, "stt_ms": ms}).encode()
        req = urllib.request.Request(agent_url, body, {"Content-Type": "application/json"})
        try:
            urllib.request.urlopen(req, timeout=2).read()
        except OSError as err:
            log.warning("agent unreachable: %s", err)


def find_output(name: str) -> int:
    """Pick the MME variant of the device: MME resamples, so any voice sample rate plays."""
    for i, d in enumerate(sd.query_devices()):
        if d["max_output_channels"] > 0 and d["name"].startswith(name) and d["hostapi"] == 0:
            return i
    raise LookupError(f"output device {name!r} not found")


def rescan_devices() -> None:
    """Make PortAudio list the devices again: it keeps the list from start-up, and the numbers in
    it go stale when headphones come or go (the voice once died with "device ID out of range")."""
    sd._terminate()
    sd._initialize()


# Nicknames as they should sound. The text chat keeps the originals.
SAY_AS = {
    "Pon4ikYT": "Пончик", "Пон4ик": "Пончик", "Pon4ik": "Пончик",
    "NetTyan": "Нетян", "Nettyan": "Нетян",
    "arcmlchikkk": "Пыхтун",
}


def say_as(text: str) -> str:
    for nick, spoken in SAY_AS.items():
        text = text.replace(nick, spoken)
    return text


ACUTE = chr(0x301)  # combining acute accent: espeak reads it as the stress mark


FIRST_SENTENCE = re.compile(r"(.+?[.!?…]+)\s+(\S.*)", re.S)


def split_first_sentence(text: str) -> "tuple[str, str]":
    """The first sentence and the rest ("" when there is one sentence only)."""
    m = FIRST_SENTENCE.fullmatch(text.strip())
    return (m.group(1), m.group(2)) if m else (text, "")


class Stress:
    """Stress marks from RUAccent: Piper's espeak dictionary misses words ("П+ыхтун", "м+ука" for
    flour) and homographs. RUAccent writes "+" before the stressed vowel, which Piper would read
    out as "плюс"; espeak takes a combining acute after the vowel. About 75 ms a line on the CPU.
    The models load in the background (about 18 s): till then, and without the package, lines go
    unmarked."""

    PLUS = re.compile(r"\+([аеёиоуыэюяАЕЁИОУЫЭЮЯ])")
    # A function, not a template: r"\1" followed by U+0301 lost the vowel in a test.

    def __init__(self, workdir: str):
        self.accent = None
        self.ready = threading.Event()  # set once loading is over, whether it worked or not
        threading.Thread(target=self._load, args=(workdir,), daemon=True).start()

    def _load(self, workdir: str) -> None:
        try:
            from ruaccent import RUAccent
            accent = RUAccent()
            accent.load(omograph_model_size="turbo3.1", use_dictionary=True, workdir=workdir)
            accent.process_all("Привет, народ.")  # the first call is slow (0.5 s live): not on a line
            self.accent = accent
            log.info("stress marks on (RUAccent, %s)", workdir)
        except Exception as err:  # no package, no models: speak as before
            log.warning("stress marks off: %s", err)
        finally:
            self.ready.set()

    def mark(self, text: str) -> str:
        if self.accent is None:
            return text
        try:
            return self.PLUS.sub(lambda m: m.group(1) + ACUTE, self.accent.process_all(text))
        except Exception as err:
            log.warning("stress marks failed (%s): %s", err, text)
            return text


class Speaker:
    REOPEN_TRIES = 15  # a second apart: the device may take a moment to come back

    def __init__(self, voice: PiperVoice, device_name: str, stress: "Stress | None" = None):
        self.voice = voice
        self.stress = stress
        self.marker = concurrent.futures.ThreadPoolExecutor(max_workers=1)  # marks the rest of a line
        self.device_name = device_name
        self.device = find_output(device_name)
        self.queue: "queue.Queue[str]" = queue.Queue()
        self.speaking = threading.Event()
        self.level = 0.0  # loudness of what is playing now, 0..1, for the avatar's mouth
        threading.Thread(target=self._run, daemon=True).start()

    def say(self, text: str) -> None:
        self.queue.put(say_as(text))

    def _run(self):
        # Never lets an error end the thread: once it did, and the bot stayed mute till a restart.
        while True:
            text = self.queue.get()
            self.speaking.set()
            try:
                try:
                    self._speak(text)
                except (sd.PortAudioError, ValueError, OSError) as err:
                    log.warning("audio device lost (%s): reopening %s", err, self.device_name)
                    if self._reopen():
                        self._speak(text)
            except Exception:
                log.exception("speak failed: %s", text)
            finally:
                self.level = 0.0
                self.speaking.clear()

    def _reopen(self) -> bool:
        """Find the device again by name in a fresh device list; False when it does not come back."""
        for _ in range(self.REOPEN_TRIES):
            try:
                rescan_devices()
                self.device = find_output(self.device_name)
                log.info("audio device back: %s", sd.query_devices(self.device)["name"])
                return True
            except (LookupError, sd.PortAudioError) as err:
                log.warning("waiting for %s: %s", self.device_name, err)
                time.sleep(1)
        log.error("audio device %s gone, line dropped", self.device_name)
        return False

    def _speak(self, text: str) -> None:
        rate = self.voice.config.sample_rate
        start = time.perf_counter()
        first_ms, seconds = None, 0.0
        # Stress marks took 0.25-0.5 s a line in a game on the same CPU: the first sentence is
        # marked alone and spoken, the rest is marked in the background while it plays.
        head, tail = split_first_sentence(text)
        marked = self.stress.mark(head) if self.stress else head
        stress_ms = (time.perf_counter() - start) * 1000
        later = self.marker.submit(self.stress.mark, tail) if self.stress and tail else None
        # Piper yields one chunk per sentence: play each as soon as it is ready, so the first
        # sound does not wait for the whole line.
        with sd.OutputStream(samplerate=rate, channels=1, dtype="int16", device=self.device) as out:
            for part in (marked, later.result() if later else tail):
                if not part:
                    continue
                for chunk in self.voice.synthesize(part):
                    if first_ms is None:
                        first_ms = (time.perf_counter() - start) * 1000
                    audio = chunk.audio_int16_array
                    seconds += len(audio) / rate
                    # In 40 ms slices: each write blocks about as long as it plays, so the level
                    # set before it follows the voice the stream hears.
                    step = rate // 25
                    for i in range(0, len(audio), step):
                        piece = audio[i:i + step]
                        rms = float(np.sqrt(np.mean(piece.astype(np.float32) ** 2))) if len(piece) else 0.0
                        self.level = min(1.0, rms / 5000.0)
                        out.write(piece.reshape(-1, 1))
        log.info("speak (first sound %d ms, stress %d ms, %.1fs audio): %s", first_ms or 0, stress_ms, seconds, text)


AVATAR_PAGE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "avatar.html")
MOODS = ("neutral", "happy", "scared", "grumpy")


class Mood:
    """The avatar's mood: set by the agent for a while, then back to neutral."""

    def __init__(self):
        self.name, self.until = "neutral", 0.0

    def set(self, name: str, seconds: float) -> None:
        self.name = name if name in MOODS else "neutral"
        self.until = time.monotonic() + max(1.0, min(seconds, 600.0))

    def now(self) -> str:
        return self.name if time.monotonic() < self.until else "neutral"


def serve(port: int, speaker: Speaker):
    mood = Mood()

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
            if self.path == "/say":
                text = str(body.get("text", "")).strip()
                if text:
                    speaker.say(text)
            elif self.path == "/mood":
                mood.set(str(body.get("mood", "neutral")), float(body.get("seconds", 8)))
            else:
                self.send_error(404)
                return
            self.send_response(204)
            self.end_headers()

        def do_GET(self):
            path = self.path.split("?", 1)[0]
            if path == "/avatar":
                with open(AVATAR_PAGE, "rb") as f:
                    self.reply(200, "text/html; charset=utf-8", f.read())
            elif path == "/avatar/state":
                state = {"speaking": speaker.speaking.is_set(), "level": round(speaker.level, 3), "mood": mood.now()}
                self.reply(200, "application/json", json.dumps(state).encode())
            else:
                self.reply(200, "application/json", b'{"status":"ok"}')

        def reply(self, code: int, kind: str, data: bytes) -> None:
            self.send_response(code)
            self.send_header("Content-Type", kind)
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(data)

        def log_message(self, *args):
            pass

    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--voice", default=r"models\piper\ru_RU-dmitri-medium.onnx")
    p.add_argument("--whisper", default="mobiuslabsgmbh/faster-whisper-large-v3-turbo")
    p.add_argument("--compute", default="int8_float16")
    p.add_argument("--device", default="CABLE Input")
    p.add_argument("--port", type=int, default=8091)
    p.add_argument("--agent", default="http://127.0.0.1:8092/voice")
    p.add_argument("--hotwords", default="Пыхтун")
    p.add_argument("--say", help="speak this once and exit (voice check)")
    p.add_argument("--stress-models", default=os.path.join(ROOT, "models", "ruaccent"), help="RUAccent models folder")
    p.add_argument("--no-stress", action="store_true", help="no RUAccent stress marks")
    p.add_argument("--bot", default=bot_nick(), help="the bot's nickname: whose game to hear through")
    p.add_argument("--game-port", type=int, default=25360, help="UDP port the mod sends voice to (voiceUdpPort)")
    args = p.parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")

    try:
        stress = None if args.no_stress else Stress(args.stress_models)
        speaker = Speaker(PiperVoice.load(args.voice), args.device, stress)
    except LookupError as err:
        raise SystemExit(str(err))
    if args.say:
        if stress:
            stress.ready.wait(60)  # a voice check should sound the way the bot will
        speaker.say(args.say)
        time.sleep(0.5)
        while not speaker.queue.empty() or speaker.speaking.is_set():
            time.sleep(0.1)
        return

    log.info("loading whisper %s (%s)", args.whisper, args.compute)
    model = WhisperModel(args.whisper, device="cuda", compute_type=args.compute)
    phrases: "queue.Queue[tuple[str, np.ndarray]]" = queue.Queue()
    hearing = Hearing(phrases)
    threading.Thread(target=transcriber, args=(phrases, model, args.agent, args.hotwords), daemon=True).start()

    # The game may start after this service (the control panel starts everything at once): its
    # packets arrive whenever it is up.
    threading.Thread(target=listen_game, args=(hearing, args.bot, args.game_port), daemon=True).start()
    log.info("speaking on %s, listening for /say on :%d", sd.query_devices(speaker.device)["name"], args.port)
    serve(args.port, speaker)


ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def bot_nick() -> str:
    """BOT_NICK from the environment or the agent's .env (only that line is read)."""
    if os.environ.get("BOT_NICK"):
        return os.environ["BOT_NICK"]
    try:
        with open(os.path.join(ROOT, ".env"), encoding="utf-8") as f:
            for line in f:
                if line.startswith("BOT_NICK="):
                    return line.split("=", 1)[1].strip()
    except OSError:
        pass
    return ""


if __name__ == "__main__":
    main()
