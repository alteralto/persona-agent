import unittest
from unittest.mock import Mock, patch

from walk_view import raise_view


class GroundViewTest(unittest.TestCase):
    def client(self):
        api = Mock()
        me = dict(name="p_agentttt", onGround=True, flying=False, inWater=False,
                  manualMs=31000, pitch=56.5)
        screen = dict(open=True, screen="class_476", title="Главное меню")
        api.getGameState.return_value = {"self": me}
        api.getOpenScreen.return_value = screen
        api.pathStatus.return_value = {"busy": True}

        def close():
            screen["open"] = False
            return True

        def rotate(yaw, pitch):
            self.assertEqual(yaw, 0)
            self.assertLessEqual(abs(pitch), 12)
            me["pitch"] += pitch
            return True

        api.closeOpenScreen.side_effect = close
        api.rotateCamera.side_effect = rotate
        return api, me, screen

    @patch("walk_view.time.sleep")
    def test_stale_server_menu_during_walk(self, sleep):
        api, me, _ = self.client()
        raise_view(api, "p_agentttt")
        api.closeOpenScreen.assert_called_once()
        self.assertLessEqual(me["pitch"], 8)

    def test_preserves_other_screens_and_manual_control(self):
        for key, value in (("title", "Магазин"), ("screen", "ChatScreen"), ("busy", False),
                           ("manualMs", 20000), ("name", "arca_pro"), ("inWater", True)):
            with self.subTest(key=key):
                api, me, screen = self.client()
                if key in screen:
                    screen[key] = value
                elif key == "busy":
                    api.pathStatus.return_value[key] = value
                else:
                    me[key] = value
                raise_view(api, "p_agentttt")
                api.closeOpenScreen.assert_not_called()
                api.rotateCamera.assert_not_called()
