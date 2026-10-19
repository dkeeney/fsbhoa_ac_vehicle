# South Enter LPR (192.168.107.138): lens and view before re-aiming, 2026-10-18 16:51

The camera doesn't report its zoom or focus position (`GetAZConfig` holds only the lens mode,
manual, and autofocus, off; the web page moves the lens by relative steps through its plugin).
So the view itself is the record:

- `138_view.jpg`: a frame of the main stream (1920 x 1080). To get back to this zoom, match the
  paver pattern and the curb: the curb's corner at the top right, the pavement's edge with the
  street across the top, the sidewalk along the right side.
- `138_GetAZConfig.xml`: lens mode and autofocus.
- `138_GetVehicleConfig.xml`: plate detection as of 16:44, after the detection area was opened to
  the top and left edges: (0,0)-(8375,8800) in 1/10000 of the picture, minimum plate size 300.
  Before the user's edit in the web page that day: (375,700)-(8375,8800), minimum plate size 600
  (`../2026-10-18_before/138_GetVehicleConfig.xml`).

## South Enter Reolink (192.168.107.141), 2026-10-19

`141_reolink_view.jpg`: the final aim at 16:00, looking south from the LPR pole through the arch to the keypad island, with the arm (down) across the lower third. Earlier tries: about 15:41 (arm on the bottom edge), 15:57 (too far down, keypad island at the top edge), 15:58 (higher; the raised arm showed on the left edge).
