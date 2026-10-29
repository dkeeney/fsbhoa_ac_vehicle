#!/bin/bash
# install_front_plate.sh: the front plate reader for vehicle_service (front_plate.go; CLAUDE.md,
# "Front plates from the NVR"). Puts a Python with fast-alpr in /opt/fsbhoa/alpr, copies
# front_plate.py there, and downloads the models (about 35 MB) into the service user's
# ~/.cache by reading one blank frame as that user. Run again to update front_plate.py.
# Usage: sudo ./tools/install_front_plate.sh [service user, default pi]
set -e
DIR=/opt/fsbhoa/alpr
SVC_USER="${1:-pi}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ "$(id -u)" -ne 0 ]; then echo "Run with sudo"; exit 1; fi
command -v ffmpeg >/dev/null || { echo "ffmpeg is needed too (apt install ffmpeg)"; exit 1; }

if [ ! -x "$DIR/bin/python" ]; then
    echo "Creating $DIR..."
    mkdir -p /opt/fsbhoa
    python3 -m venv "$DIR"
fi
echo "Installing fast-alpr..."
"$DIR/bin/pip" install -q "fast-alpr[onnx]==0.4.0"
install -m 644 "$HERE/front_plate.py" "$DIR/front_plate.py"

echo "Downloading the models as $SVC_USER..."
TMP=$(mktemp -d)
"$DIR/bin/python" -I -c "import numpy, cv2; cv2.imwrite('$TMP/blank.jpg', numpy.zeros((360, 640, 3), numpy.uint8))"
chown -R "$SVC_USER" "$TMP"
sudo -u "$SVC_USER" -H "$DIR/bin/python" -I "$DIR/front_plate.py" "$TMP/crop.jpg" "$TMP/blank.jpg" 2>/dev/null
rm -rf "$TMP"
echo "Done. vehicle_service uses it for the next car without a plate read (no restart needed)."
