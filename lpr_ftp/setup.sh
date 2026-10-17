#!/bin/bash
# lpr_ftp/setup.sh
# Purpose: Set up the FTP server (vsftpd) that receives the LPR cameras' uploads.
# Usage:   sudo LPR_FTP_PASSWORD='...' ./lpr_ftp/setup.sh <camera IP> [<camera IP> ...]
#          (without LPR_FTP_PASSWORD it asks; the password is the one set in the cameras'
#          Network > TCP/IP > FTP settings, user specolpr)
# Safe to run again: it replaces its own files and lines, and keeps the account's password
# unless one is given.

set -e
BASE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FTP_USER=specolpr
DROP_DIR=/var/lib/fsbhoa/lpr_drop

if [ "$(id -u)" -ne 0 ]; then echo "Run with sudo."; exit 1; fi
if [ $# -eq 0 ]; then echo "Usage: sudo $0 <camera IP> [<camera IP> ...]"; exit 1; fi
for ip in "$@"; do
    if ! [[ "$ip" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]; then echo "Not an IPv4 address: $ip"; exit 1; fi
done

# 1. vsftpd
if ! command -v vsftpd >/dev/null; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y vsftpd
fi

# 2. The upload account: no shell, home is the drop folder, group www-data (vehicle_service)
if ! id "$FTP_USER" >/dev/null 2>&1; then
    useradd --system --gid www-data --home-dir "$DROP_DIR" --no-create-home --shell /usr/sbin/nologin "$FTP_USER"
    echo "Created user $FTP_USER"
    NEED_PASSWORD=1
fi
if [ -z "$LPR_FTP_PASSWORD" ] && [ -n "$NEED_PASSWORD" ]; then
    read -r -s -p "FTP password for $FTP_USER (as set in the cameras): " LPR_FTP_PASSWORD; echo
fi
if [ -n "$LPR_FTP_PASSWORD" ]; then
    echo "$FTP_USER:$LPR_FTP_PASSWORD" | chpasswd
    echo "Set the password for $FTP_USER"
fi

# 3. The drop folder, and its 7-day cleanup
install -m 644 "$BASE_DIR/fsbhoa-lpr-drop.tmpfiles" /etc/tmpfiles.d/fsbhoa-lpr-drop.conf
systemd-tmpfiles --create /etc/tmpfiles.d/fsbhoa-lpr-drop.conf

# 4. vsftpd's settings, its PAM service and the one account allowed to log in
[ -f /etc/vsftpd.conf.dist ] || cp /etc/vsftpd.conf /etc/vsftpd.conf.dist
install -m 644 "$BASE_DIR/vsftpd.conf" /etc/vsftpd.conf
install -m 644 "$BASE_DIR/vsftpd_lpr.pam" /etc/pam.d/vsftpd_lpr
echo "$FTP_USER" > /etc/vsftpd.userlist
chmod 644 /etc/vsftpd.userlist

# 5. Only the cameras (and this machine, for testing) may connect
sed -i '/^vsftpd:/d' /etc/hosts.allow /etc/hosts.deny
echo "vsftpd: 127.0.0.1 $*" >> /etc/hosts.allow
echo "vsftpd: ALL" >> /etc/hosts.deny

systemctl enable vsftpd
systemctl restart vsftpd
echo "vsftpd is $(systemctl is-active vsftpd) on port 2121; uploads go to $DROP_DIR; cameras: $*"
