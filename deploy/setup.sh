#!/bin/bash
set -e

INSTALL_DIR="${PUSHBOY_DIR:-/opt/pushboy}"

echo "==> Building pushboy binary..."
cd "$INSTALL_DIR"
go build -o pushboy ./cmd/pushboy

echo "==> Installing systemd service..."
sudo cp deploy/pushboy.service /etc/systemd/system/pushboy.service

# Update paths in service file to match install directory
sudo sed -i "s|/opt/pushboy|$INSTALL_DIR|g" /etc/systemd/system/pushboy.service

echo "==> Reloading systemd..."
sudo systemctl daemon-reload

echo "==> Enabling pushboy service..."
sudo systemctl enable pushboy

echo "==> Starting pushboy..."
sudo systemctl start pushboy

echo "==> Checking status..."
sudo systemctl status pushboy --no-pager

echo ""
echo "Setup complete!"
echo ""
echo "Useful commands:"
echo "  View logs:    sudo journalctl -u pushboy -f"
echo "  Restart:      sudo systemctl restart pushboy"
echo "  Stop:         sudo systemctl stop pushboy"
echo "  Status:       sudo systemctl status pushboy"
