#!/usr/bin/env python3
"""Simulate a 26-bit Wiegand card swipe into the testbed DoorKing 1838.

Runs on the workbench Raspberry Pi 3B (192.168.1.210), whose GPIO pins drive the
controller's Wiegand data lines through transistors (see CLAUDE.md, "Testbed workbench").
It needs RPi.GPIO, so it only runs on that Pi; copy it there to use it.

Usage:
    python3 wiegand_sim.py                      # facility 0, card 10018 (the original test card)
    python3 wiegand_sim.py --facility 186 --card 6549   # same as the workbench fob
    python3 wiegand_sim.py --bits 10000000000100111001000100   # send raw bits as given

26-bit format (H10301): even parity, 8-bit facility code, 16-bit card number, odd parity.
The leading even parity bit covers the first 12 data bits; the trailing odd parity bit
covers the last 12.
"""
import argparse
import sys
import time

# Map DoorKing wires to Pi GPIO pins (BCM numbering)
D0_PIN = 17  # Controls the Green Wire (L0)
D1_PIN = 27  # Controls the White Wire (L1)

PULSE_SECONDS = 0.00005  # Hold each pulse for 50 microseconds
GAP_SECONDS = 0.002      # Wait 2 milliseconds between pulses


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


def swipe(bits):
    import RPi.GPIO as GPIO

    GPIO.setmode(GPIO.BCM)
    GPIO.setwarnings(False)
    GPIO.setup(D0_PIN, GPIO.OUT, initial=GPIO.LOW)
    GPIO.setup(D1_PIN, GPIO.OUT, initial=GPIO.LOW)
    try:
        for bit in bits:
            pin = D0_PIN if bit == "0" else D1_PIN
            GPIO.output(pin, GPIO.HIGH)  # Turn on transistor, pulls the DoorKing line low
            time.sleep(PULSE_SECONDS)
            GPIO.output(pin, GPIO.LOW)   # Release
            time.sleep(GAP_SECONDS)
    finally:
        GPIO.cleanup()


def main():
    parser = argparse.ArgumentParser(description="Simulate a 26-bit Wiegand card swipe.")
    parser.add_argument("--facility", type=int, default=0, help="facility code, 0-255 (default 0)")
    parser.add_argument("--card", type=int, default=10018, help="card number, 0-65535 (default 10018)")
    parser.add_argument("--bits", help="send this raw bit string instead (overrides --facility/--card)")
    args = parser.parse_args()

    if args.bits:
        if set(args.bits) - {"0", "1"}:
            sys.exit("--bits must contain only 0 and 1")
        bits = args.bits
        print(f"Swiping raw {len(bits)}-bit string {bits}")
    else:
        try:
            bits = wiegand26(args.facility, args.card)
        except ValueError as e:
            sys.exit(str(e))
        print(f"Swiping facility {args.facility}, card {args.card} ({bits})")

    swipe(bits)
    print("Swipe complete!")


if __name__ == "__main__":
    main()
