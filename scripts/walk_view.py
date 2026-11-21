"""Raise the view on the bot's ground walk using the installed UnionClef camera API."""
import logging
import sys
import time

from py4j.java_gateway import GatewayParameters, JavaGateway
from py4j.protocol import Py4JError


def raise_view(api, nick):
    for step in range(10):
        me = api.getGameState().get("self")
        if (not me or me.get("name") != nick or not me.get("onGround")
                or me.get("inWater") or me.get("flying")
                or (me.get("manualMs") or 0) < 5000):
            return
        screen = api.getOpenScreen()
        if screen.get("open"):
            # SkyBars opens this inventory menu at spawn. A walk can stay busy
            # for minutes behind it. Dismiss only this menu during an autonomous route.
            if (step or me.get("manualMs") < 30000 or not api.pathStatus().get("busy")
                    or screen.get("screen") not in ("class_476", "GenericContainerScreen")
                    or screen.get("title") != "Главное меню"):
                return
            if not api.closeOpenScreen():
                return
            time.sleep(0.15)
            continue
        pitch = me.get("pitch")
        if pitch is None or pitch <= (35 if step == 0 else 8):
            return
        if not api.rotateCamera(0.0, max(-12.0, 5.0 - pitch)):
            return
        time.sleep(0.1)


def main(nick, base_port):
    # UnionClef increments its gateway port by two for neighbouring clients.
    logging.getLogger("py4j").setLevel(logging.CRITICAL)
    for port in range(base_port, base_port + 40, 2):
        gateway = JavaGateway(gateway_parameters=GatewayParameters(port=port, read_timeout=0.5))
        try:
            if gateway.entry_point.getUsername() == nick:
                raise_view(gateway.entry_point, nick)
                return 0
        except (Py4JError, OSError):
            continue
        finally:
            gateway.close()
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1], int(sys.argv[2]) if len(sys.argv) > 2 else 25333))
