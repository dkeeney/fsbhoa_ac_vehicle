#!/usr/bin/env python3
"""Simulate a Wiegand card reader or keypad into the testbed DoorKing 1838.

Runs on the workbench Raspberry Pi 3B (192.168.1.210), whose GPIO pins drive the
controller's Wiegand data lines through transistors (see CLAUDE.md, "Testbed workbench").
It needs RPi.GPIO, so it only runs on that Pi; copy it there to use it.

Usage:
    python3 wiegand_sim.py                      # facility 0, card 10018 (the original test card)
    python3 wiegand_sim.py --facility 186 --card 6549   # same as the workbench fob
    python3 wiegand_sim.py --keys 1234#         # keypad, 4 bits per key
    python3 wiegand_sim.py --keys 1234# --key-bits 8    # keypad, 8 bits per key
    python3 wiegand_sim.py --bits 10000000000100111001000100   # send raw bits as given

Cards use the 26-bit format (H10301): even parity, 8-bit facility code, 16-bit card
number, odd parity. The leading even parity bit covers the first 12 data bits; the
trailing odd parity bit covers the last 12.

Keypads send one burst per key press. Keys 0-9 are their value, * is 10 and # is 11.
With 4 bits per key that value is sent as is; with 8 bits per key its inverse comes
first (key 1 is 1110 0001), so the controller can reject a garbled key. Keypads that
buffer the whole code and send it as one 26-bit card are simulated with --card.
"""
import argparse
import sys
import time

# Map DoorKing wires to Pi GPIO pins (BCM numbering)
D0_PIN = 17  # Controls the Green Wire (L0)
D1_PIN = 27  # Controls the White Wire (L1)

PULSE_SECONDS = 0.00005  # Hold each pulse for 50 microseconds
GAP_SECONDS = 0.002      # Wait 2 milliseconds between pulses
KEY_GAP_SECONDS = 0.3    # Wait between key presses, like a person typing

KEY_VALUES = {**{str(d): d for d in range(10)}, "*": 10, "#": 11}


def wiegand26(facility, card):
    """Return the 26-bit string for a facility code (0-255) and card number (0-65535)."""
    if not 0 <= facility <= 255:
        raise ValueError(f"facility code {facility} is outside 0-255")
    if not 0 <= card <= 65535:
        raise ValueError(f"card number {card} is outside 0-65535")
    data = f"{facility:08b}{card:016b}"
    even = "1" if data[:12].count("1") % 2 else "0"
    odd = "0" if data[12:].count("1") % 2 else "1"
    return even + data + odd


def key_bursts(keys, bits_per_key):
    """Return one bit string per key press, in the 4- or 8-bit keypad format."""
    bursts = []
    for key in keys:
        if key not in KEY_VALUES:
            raise ValueError(f"key {key!r} is not 0-9, * or #")
        value = KEY_VALUES[key]
        if bits_per_key == 8:
            value |= (~value & 0xF) << 4
        bursts.append(f"{value:0{bits_per_key}b}")
    return bursts


def send(bursts):
    """Pulse each burst onto the data lines, pausing between bursts."""
    import RPi.GPIO as GPIO

    GPIO.setmode(GPIO.BCM)
    GPIO.setwarnings(False)
    GPIO.setup(D0_PIN, GPIO.OUT, initial=GPIO.LOW)
    GPIO.setup(D1_PIN, GPIO.OUT, initial=GPIO.LOW)
    try:
        for i, bits in enumerate(bursts):
            if i:
                time.sleep(KEY_GAP_SECONDS)
            for bit in bits:
                pin = D0_PIN if bit == "0" else D1_PIN
                GPIO.output(pin, GPIO.HIGH)  # Turn on transistor, pulls the DoorKing line low
                time.sleep(PULSE_SECONDS)
                GPIO.output(pin, GPIO.LOW)   # Release
                time.sleep(GAP_SECONDS)
    finally:
        GPIO.cleanup()


def main():
    parser = argparse.ArgumentParser(description="Simulate a Wiegand card reader or keypad.")
    parser.add_argument("--facility", type=int, default=0, help="facility code, 0-255 (default 0)")
    parser.add_argument("--card", type=int, default=10018, help="card number, 0-65535 (default 10018)")
    parser.add_argument("--keys", help="press these keypad keys instead (0-9, *, #), e.g. 1234#")
    parser.add_argument("--key-bits", type=int, choices=(4, 8), default=4,
                        help="bits per key press for --keys (default 4)")
    parser.add_argument("--bits", help="send this raw bit string instead")
    args = parser.parse_args()

    try:
        if args.bits:
            if set(args.bits) - {"0", "1"}:
                raise ValueError("--bits must contain only 0 and 1")
            bursts = [args.bits]
            print(f"Sending raw {len(args.bits)}-bit string {args.bits}")
        elif args.keys:
            bursts = key_bursts(args.keys, args.key_bits)
            print(f"Pressing keys {args.keys} ({args.key_bits} bits per key: {' '.join(bursts)})")
        else:
            bursts = [wiegand26(args.facility, args.card)]
            print(f"Swiping facility {args.facility}, card {args.card} ({bursts[0]})")
    except ValueError as e:
        sys.exit(str(e))

    send(bursts)
    print("Done")


if __name__ == "__main__":
    main()
