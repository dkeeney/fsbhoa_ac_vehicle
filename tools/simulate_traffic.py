#!/usr/bin/env python3
"""Simulate vehicle gate traffic on the testbed so the live monitor has something to show.

Each simulated car is one of: a windshield-tag entry, a keypad-PIN entry,
a visitor let in through a directory code, a circumvention (loop tripped with no credential), or an entry with no plate read.
Photos are copies of real Speco captures (plate + scene pairs) already in the
FTP drop folder. Credentials are random active DoorKing credentials from the
database.

Two ways to send the traffic:
  --via service    (default) Drive vehicle_service the way the hardware does:
                   Shelly loop webhook, DoorKing webhook, and Speco files dropped
                   into the watch folder. Tests the whole pipeline. Cars are sent
                   one correlation window apart, since the service merges inputs
                   that arrive within one window (TODO item 6). Cardholder names
                   won't show, because the service doesn't send auth_type yet
                   (TODO item 7).
  --via wordpress  POST finished events straight to /vehicle-event, including
                   auth_type, so cardholder names show. Skips vehicle_service.

Simulated rows use gate names starting with "SIM ", and --cleanup deletes them.
Via the service, a circumvention has no DoorKing input, so the service stores it
with an empty gate name. --cleanup therefore also deletes rows with an empty gate
name, which on the testbed only come from tests.

Runs only where FSBHOA_AC_ENVIRONMENT is 'testbed' (fails closed).

Usage:
  tools/simulate_traffic.py                       # 10 cars through vehicle_service
  tools/simulate_traffic.py --via wordpress --count 0 --interval 4   # until Ctrl-C
  tools/simulate_traffic.py --cleanup
"""

import argparse
import base64
import json
import os
import random
import re
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime

CONFIG_PATH = '/var/lib/fsbhoa/vehicle_service.json'
WP_PATH = '/var/www/html'
WATCH_DIR = '/home/pi/lpr_ftp_drop'  # Hard-coded in vehicle_service/config.go too (TODO item 16)
SIM_SUBDIR = 'sim'
SIM_GATES = ['SIM North', 'SIM South']
SIM_ROWS = "(gate_identifier LIKE 'SIM %' OR gate_identifier = '')"
PLATE_RE = re.compile(r'^(VEHICE_\d{4}-\d{2}-\d{2}-\d{2}-\d{2}-\d{2}-\d+)_plate_([A-Z0-9]+)\.jpg$')

# Scenario weights: (name, weight)
SCENARIOS = [
    ('windshield', 70),
    ('keypad', 10),
    ('directory', 5),
    ('circumvention', 10),
    ('no_plate', 5),
]


def log(msg):
    print(f'[{datetime.now():%H:%M:%S}] {msg}', flush=True)


def wp(*args):
    """Run a wp-cli command against the local site and return stdout."""
    result = subprocess.run(['wp', f'--path={WP_PATH}', *args],
                            capture_output=True, text=True)
    if result.returncode != 0:
        sys.exit(f'wp-cli failed: {" ".join(args)}\n{result.stderr.strip()}')
    return result.stdout


def require_testbed():
    env = wp('eval', 'echo defined("FSBHOA_AC_ENVIRONMENT") ? FSBHOA_AC_ENVIRONMENT : "";').strip()
    if env != 'testbed':
        sys.exit(f'Refusing to run: FSBHOA_AC_ENVIRONMENT is {env or "not set"!r}, not \'testbed\'.')


def load_config():
    try:
        with open(CONFIG_PATH) as f:
            return json.load(f)
    except (OSError, ValueError) as e:
        sys.exit(f'Cannot read {CONFIG_PATH}: {e}')


def find_sample_pairs():
    """Return [(plate_path, src_path, plate_text)] for real captures already in the drop folder."""
    sim_dir = os.path.join(WATCH_DIR, SIM_SUBDIR)
    pairs = []
    for root, _dirs, files in os.walk(WATCH_DIR):
        if root.startswith(sim_dir):
            continue
        for name in files:
            m = PLATE_RE.match(name)
            if not m:
                continue
            src = os.path.join(root, m.group(1) + '_src.jpg')
            if os.path.isfile(src):
                pairs.append((os.path.join(root, name), src, m.group(2)))
    if not pairs:
        sys.exit(f'No plate/scene photo pairs found under {WATCH_DIR}.')
    return pairs


def load_credentials():
    """Return {'DK_WINDSHIELD': [...], 'DK_ENTRY_CODE': [...], 'DK_DIR_CODE': [...]} of active credential values."""
    out = wp('db', 'query', '--skip-column-names',
             "SELECT credential_type, credential_value FROM ac_credentials "
             "WHERE status = 'active' AND credential_type IN ('DK_WINDSHIELD', 'DK_ENTRY_CODE', 'DK_DIR_CODE') "
             "AND credential_value <> '' ORDER BY RAND() LIMIT 600")
    creds = {'DK_WINDSHIELD': [], 'DK_ENTRY_CODE': [], 'DK_DIR_CODE': []}
    for line in out.splitlines():
        parts = line.split('\t')
        if len(parts) == 2 and parts[0] in creds:
            creds[parts[0]].append(parts[1])
    for ctype, values in creds.items():
        if not values:
            sys.exit(f'No active {ctype} credentials found.')
    return creds


def pick_car(pairs, creds):
    scenario = random.choices([s for s, _ in SCENARIOS], weights=[w for _, w in SCENARIOS])[0]
    plate_path, src_path, plate = random.choice(pairs)
    car = {
        'scenario': scenario,
        'gate': random.choice(SIM_GATES),
        'plate_path': plate_path,
        'src_path': src_path,
        'plate': plate,
        'auth_type': '',
        'auth_id': '',
    }
    if scenario == 'windshield':
        car['auth_type'], car['auth_id'] = 'DK_WINDSHIELD', random.choice(creds['DK_WINDSHIELD'])
    elif scenario == 'keypad':
        car['auth_type'], car['auth_id'] = 'DK_ENTRY_CODE', random.choice(creds['DK_ENTRY_CODE'])
    elif scenario == 'directory':
        car['auth_type'], car['auth_id'] = 'DK_DIR_CODE', random.choice(creds['DK_DIR_CODE'])
    elif scenario == 'no_plate':
        car['auth_type'], car['auth_id'] = 'DK_WINDSHIELD', random.choice(creds['DK_WINDSHIELD'])
        car['plate_path'] = car['src_path'] = car['plate'] = None
    return car


def describe(car):
    return (f"{car['scenario']:<13} {car['gate']:<10} plate={car['plate'] or '-':<8} "
            f"{car['auth_type'] or 'no auth'} {car['auth_id']}")


# --- via vehicle_service -------------------------------------------------------

def http_get(url):
    with urllib.request.urlopen(url, timeout=5) as resp:
        return resp.status


def drop_file(src, dest_dir, name):
    """Copy src into the watch folder under a temp name, then rename so the watcher sees a complete file."""
    tmp = os.path.join(dest_dir, '.' + name + '.part')
    shutil.copyfile(src, tmp)
    os.rename(tmp, os.path.join(dest_dir, name))


def send_via_service(car, cfg, sim_dir):
    base = f"http://{cfg.get('daemon_host', '127.0.0.1')}:{cfg.get('daemon_port', 8088)}"

    http_get(base + '/webhook/loop?state=on')
    time.sleep(0.5)

    if car['auth_id']:
        query = urllib.parse.urlencode({'device': car['gate'], 'code': car['auth_id']})
        http_get(base + '/webhook/doorking?' + query)
        time.sleep(0.5)

    if car['plate']:
        stamp = datetime.now().strftime('%Y-%m-%d-%H-%M-%S-') + f'{datetime.now().microsecond // 1000:03d}'
        prefix = f'VEHICE_{stamp}'
        # The scene photo must be in place before the plate file, which is when the service looks for it.
        drop_file(car['src_path'], sim_dir, f'{prefix}_src.jpg')
        drop_file(car['plate_path'], sim_dir, f"{prefix}_plate_{car['plate']}.jpg")
        time.sleep(0.5)

    http_get(base + '/webhook/loop?state=off')


# --- via WordPress -------------------------------------------------------------

def b64_file(path):
    if not path:
        return ''
    with open(path, 'rb') as f:
        return base64.b64encode(f.read()).decode('ascii')


def send_via_wordpress(car, cfg):
    payload = {
        'event_uid': f'sim_{time.time_ns()}',
        'environment': cfg.get('environment', ''),
        'gate_identifier': car['gate'],
        'auth_id': car['auth_id'],
        'auth_type': car['auth_type'],
        'lpr_plate': car['plate'] or '',
        'lpr_confidence': random.randint(80, 99) if car['plate'] else None,
        'is_circumvention': 1 if car['scenario'] == 'circumvention' else 0,
        'lpr_image_b64': b64_file(car['plate_path']),
        'context_image_b64': b64_file(car['src_path']),
        'raw_details': {'simulated': True, 'scenario': car['scenario']},
    }
    req = urllib.request.Request(
        'http://127.0.0.1/wp-json/fsbhoa/v1/vehicle-event',
        data=json.dumps(payload).encode('utf-8'),
        method='POST',
        headers={
            'Content-Type': 'application/json',
            'Host': cfg.get('wordpress_host', '127.0.0.1'),
            'X-API-KEY': cfg.get('api_key', ''),
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            body = json.loads(resp.read() or b'{}')
            return f"stored as vehicle_log_id {body.get('vehicle_log_id')}"
    except urllib.error.HTTPError as e:
        sys.exit(f'WordPress rejected the event: HTTP {e.code} {e.read().decode(errors="replace")}')


# --- main ----------------------------------------------------------------------

def cleanup():
    count = wp('db', 'query', '--skip-column-names',
               f"SELECT COUNT(*) FROM ac_vehicle_log WHERE {SIM_ROWS}").strip()
    wp('db', 'query', f"DELETE FROM ac_vehicle_log WHERE {SIM_ROWS}")
    sim_dir = os.path.join(WATCH_DIR, SIM_SUBDIR)
    if os.path.isdir(sim_dir):
        for name in os.listdir(sim_dir):
            os.remove(os.path.join(sim_dir, name))
    log(f'Deleted {count} simulated rows from ac_vehicle_log and emptied {sim_dir}.')


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--via', choices=['service', 'wordpress'], default='service',
                        help='send through vehicle_service (default) or straight to WordPress')
    parser.add_argument('--count', type=int, default=10, help='number of cars; 0 = until Ctrl-C (default 10)')
    parser.add_argument('--interval', type=float, default=None,
                        help='seconds between cars (default: correlation window + 3 via service, 5 via wordpress)')
    parser.add_argument('--cleanup', action='store_true', help='delete simulated rows and dropped files, then exit')
    args = parser.parse_args()

    require_testbed()

    if args.cleanup:
        cleanup()
        return

    cfg = load_config()
    pairs = find_sample_pairs()
    creds = load_credentials()

    window = int(cfg.get('correlation_window', 15))
    if args.via == 'service':
        interval = args.interval if args.interval is not None else window + 3
        if interval <= window:
            log(f'Warning: interval {interval}s is within the {window}s correlation window, so cars will merge.')
        sim_dir = os.path.join(WATCH_DIR, SIM_SUBDIR)
        if not os.path.isdir(sim_dir):
            os.makedirs(sim_dir)
            time.sleep(1)  # Let the watcher add the new folder before files arrive
    else:
        interval = args.interval if args.interval is not None else 5

    log(f'{len(pairs)} photo pairs, {len(creds["DK_WINDSHIELD"])} windshield tags, '
        f'{len(creds["DK_ENTRY_CODE"])} PINs, {len(creds["DK_DIR_CODE"])} directory codes. Sending via {args.via}, one car every {interval:g}s.')

    sent = 0
    try:
        while args.count == 0 or sent < args.count:
            car = pick_car(pairs, creds)
            if args.via == 'service':
                send_via_service(car, cfg, sim_dir)
                log(f'#{sent + 1} {describe(car)}  (posts after the {window}s window)')
            else:
                result = send_via_wordpress(car, cfg)
                log(f'#{sent + 1} {describe(car)}  {result}')
            sent += 1
            if args.count == 0 or sent < args.count:
                time.sleep(interval)
    except KeyboardInterrupt:
        print()
    except urllib.error.URLError as e:
        sys.exit(f'Cannot reach vehicle_service: {e.reason}')

    if args.via == 'service' and sent:
        log(f'Sent {sent} cars. The last one reaches WordPress about {window}s from now.')
    else:
        log(f'Sent {sent} cars.')


if __name__ == '__main__':
    main()
