# Systemd Service Files for SwarmKit and SwarmCracker
#
# These service files are used to run SwarmKit managers and workers as system services.
#
# Installation:
#   1. Copy the appropriate service file to /etc/systemd/system/
#      sudo cp swarmd-manager.service /etc/systemd/system/   # For managers
#      sudo cp swarmd-worker.service /etc/systemd/system/    # For workers
#
#   2. For workers, create the tokens environment file:
#      sudo tee /etc/swarmcracker/tokens.env > /dev/null <<EOF
#      MANAGER_IP=192.168.1.10
#      WORKER_TOKEN=SWMTKN-1-xxx...yyy
#      EOF
#
#   3. Reload systemd and enable the service:
#      sudo systemctl daemon-reload
#      sudo systemctl enable swarmd-manager  # or swarmd-worker
#      sudo systemctl start swarmd-manager   # or swarmd-worker
#
#   4. Check status:
#      sudo systemctl status swarmd-manager  # or swarmd-worker
#
# Files in this directory:
#   - swarmd-manager.service: Manager daemon (for manager nodes)
#   - swarmd-worker.service: Worker daemon with SwarmCracker (for worker nodes)
#
# Notes:
#   - Manager nodes typically run swarmd-manager.service
#   - Worker nodes run swarmd-worker.service
#   - SwarmCracker is invoked automatically by swarmd via the executor interface
#   - No separate SwarmCracker service is needed
#
# Customization:
#   - Edit ExecStart lines to change flags
#   - Modify resource limits if needed
#   - Adjust security settings for your environment
#
# Logs:
#   View logs with: journalctl -u swarmd-manager -f
#   View logs with: journalctl -u swarmd-worker -f
#
