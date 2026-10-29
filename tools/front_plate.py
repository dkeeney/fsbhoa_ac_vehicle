"""front_plate.py <crop out> <frame>...: the front plate of a car the LPR camera didn't read, from
frames of the overview camera's recording (vehicle_service/front_plate.go; CLAUDE.md, "Front
plates from the NVR").

Reads each frame, in order, with fast-alpr (a YOLOv9 plate detector and a plate OCR, ONNX on the
CPU) on four overlapping quarters, since a plate is only about 120 pixels wide in a 4K frame and
the detector works at 608. Stops once one text has been read in MIN_FRAMES frames with a mean
confidence of at least MIN_CONFIDENCE, and prints JSON: {"plate", "confidence", "frames", "read"}
("plate" empty when no text got there; "read" the frames that read it) and writes that plate's
best crop to <crop out>. One text across frames is what tells a plate from a blur: a blurred
plate reads differently in each frame (2026-10-29, 14 unread cars at South Enter).
"""
import sys, json, collections
import cv2
from fast_alpr import ALPR

MIN_FRAMES = 2
MIN_CONFIDENCE = 0.9
TILE_W, TILE_H = 2240, 1260

def tiles(img):
    h, w = img.shape[:2]
    for y in sorted({0, max(0, h - TILE_H)}):
        for x in sorted({0, max(0, w - TILE_W)}):
            yield x, y, img[y:y + TILE_H, x:x + TILE_W]

def main():
    out, frames = sys.argv[1], sys.argv[2:]
    alpr = ALPR(detector_model='yolo-v9-s-608-license-plate-end2end', ocr_model='cct-s-v2-global-model',
                detector_conf_thresh=0.3)
    confs = collections.defaultdict(list)  # text -> confidence in each frame that read it
    best = {}                              # text -> (confidence, crop)
    for f in frames:
        img = cv2.imread(f)
        if img is None:
            continue
        seen = {}
        for x, y, t in tiles(img):
            for r in alpr.predict(t):
                if r.ocr is None or not r.ocr.text:
                    continue
                text = r.ocr.text.strip('_ ').replace('_', '')
                if len(text) < 2:
                    continue
                c = r.ocr.confidence
                c = float(sum(c) / len(c)) if isinstance(c, (list, tuple)) else float(c)
                if c > seen.get(text, 0):
                    seen[text] = c
                    if c > best.get(text, (0, None))[0]:
                        b = r.detection.bounding_box
                        best[text] = (c, img[max(0, y + b.y1 - 20):y + b.y2 + 20, max(0, x + b.x1 - 30):x + b.x2 + 30].copy())
        for text, c in seen.items():
            confs[text].append(c)
        done = [t for t, cs in confs.items() if len(cs) >= MIN_FRAMES and sum(cs) / len(cs) >= MIN_CONFIDENCE]
        if done:
            text = max(done, key=lambda t: sum(confs[t]))
            cv2.imwrite(out, best[text][1])
            cs = confs[text]
            print(json.dumps({'plate': text, 'confidence': round(sum(cs) / len(cs), 3), 'frames': len(frames), 'read': len(cs)}))
            return
    print(json.dumps({'plate': '', 'confidence': 0, 'frames': len(frames), 'read': 0,
                      'seen': sorted(confs, key=lambda t: -sum(confs[t]))[:3]}))

if __name__ == '__main__':
    main()
