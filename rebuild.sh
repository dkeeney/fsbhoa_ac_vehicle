#!/bin/bash
# rebuild.sh
# Purpose: Compile this service with strict fsbhoa_xxxx naming.
# Usage: ./rebuild.sh       (Builds only)
#        ./rebuild.sh install (Builds, Installs to /usr/local/bin, and Restarts services)

# Run from anywhere: paths are relative to this script
BASE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "--- Starting Build Process ---"

# --- Vehicle SERVICE ---
echo "1. Building Vehicle Service..."
cd "$BASE_DIR/vehicle_service" || exit
go build -o fsbhoa_vehicle .
if [ $? -eq 0 ]; then echo "   [OK] fsbhoa_vehicle built"; else echo "   [FAIL] Vehicle build failed"; exit 1; fi


# --- OPTIONAL: INSTALL ---
if [ "$1" == "install" ]; then
    echo ""
    echo "--- Installing and Restarting Services (Sudo Required) ---"
    
    # 1. STOP services to release the file lock
    echo "Stopping services..."
    sudo systemctl stop fsbhoa_vehicle
    
    # 2. COPY new binaries
    echo "Copying binaries..."
    sudo cp "$BASE_DIR/vehicle_service/fsbhoa_vehicle" /usr/local/bin/
    
    # 3. SET PERMISSIONS (Root ownership is safer for system binaries)
    echo "Setting permissions..."
    sudo chown root:root /usr/local/bin/fsbhoa_*
    sudo chmod 755 /usr/local/bin/fsbhoa_*

    # 4. INSTALL the systemd unit if it is new or changed
    UNIT_SRC="$BASE_DIR/vehicle_service/fsbhoa_vehicle.service"
    UNIT_DST=/etc/systemd/system/fsbhoa_vehicle.service
    if ! cmp -s "$UNIT_SRC" "$UNIT_DST"; then
        echo "Installing systemd unit..."
        sudo cp "$UNIT_SRC" "$UNIT_DST"
        sudo systemctl daemon-reload
        sudo systemctl enable fsbhoa_vehicle
    fi
    
    # 5. START services
    echo "Starting Systemd Services..."
    sudo systemctl start fsbhoa_vehicle
    
    echo "--- Install Complete ---"
    # Brief pause to let them startup before checking status
    sleep 1
    sudo systemctl status fsbhoa_* --no-pager | grep "Active:"
else
    echo ""
    echo "--- Build Complete (No Install) ---"
    echo "To install and restart services, run: ./rebuild.sh install"
fi
