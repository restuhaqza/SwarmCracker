# SwarmCracker on DigitalOcean - Quick Start Guide

## 🌊 Why DigitalOcean?

- ✅ **More resources** - No host machine constraints (4-8GB RAM per node)
- ✅ **Better performance** - Native KVM, no nested virtualization
- ✅ **Real networking** - True cloud environment
- ✅ **Persistent** - Survives host reboots
- ✅ **Accessible anywhere** - Access from any machine

## 📋 Prerequisites

### 1. DigitalOcean Account
- Sign up at https://www.digitalocean.com/
- Add payment method (credit/PayPal)

### 2. API Token
1. Go to https://cloud.digitalocean.com/settings/api/tokens
2. Click "Generate New Token"
3. Give it a name (e.g., "vagrant-swarmcracker")
4. Select **Read & Write** scope
5. Copy the token

### 3. SSH Key (Recommended)
1. Generate SSH key (if you don't have one):
   ```bash
   ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519_do -C "vagrant@digitalocean"
   ```

2. Add to DigitalOcean:
   - Go to https://cloud.digitalocean.com/settings/security
   - Click "Add SSH Key"
   - Copy your public key: `cat ~/.ssh/id_ed25519_do.pub`
   - Paste and add

3. Note your key name (e.g., "vagrant@digitalocean")

## 🚀 Setup

### Step 1: Install Vagrant Plugin
```bash
cd /home/kali/.openclaw/workspace/projects/swarmcracker/test-automation

# Install the plugin
vagrant plugin install vagrant-digitalocean
```

### Step 2: Configure Environment
```bash
# Set your API token
export DIGITAL_OCEAN_TOKEN=your_token_here

# (Optional) Set your SSH key name
export DIGITAL_OCEAN_SSH_KEY=vagrant@digitalocean
```

**💡 Pro tip:** Add these to your `~/.bashrc` for persistence:
```bash
echo 'export DIGITAL_OCEAN_TOKEN=your_token_here' >> ~/.bashrc
echo 'export DIGITAL_OCEAN_SSH_KEY=vagrant@digitalocean' >> ~/.bashrc
source ~/.bashrc
```

### Step 3: Run Setup Script
```bash
./setup-digitalocean.sh
```

This will:
- ✅ Verify your API token
- ✅ Install vagrant-digitalocean plugin
- ✅ Backup your VirtualBox Vagrantfile
- ✅ Switch to DigitalOcean configuration

### Step 4: Deploy Cluster
```bash
# Start all nodes
vagrant up

# Watch them spin up (takes ~2-3 minutes)
vagrant status
```

## 🎮 Common Commands

### Cluster Management
```bash
# Start all nodes
vagrant up

# Start specific node
vagrant up manager
vagrant up worker1

# Stop all nodes (keeps droplets but stops them - still billed!)
vagrant halt

# Destroy all nodes (stops billing!)
vagrant destroy -f

# Check status
vagrant status

# SSH into a node
vagrant ssh manager
vagrant ssh worker1
```

### Cluster Testing
```bash
# Check cluster health
vagrant ssh manager -c "
  sudo swarmctl -s /var/run/swarmkit/swarm.sock node ls
"

# List services
vagrant ssh manager -c "
  sudo swarmctl -s /var/run/swarmkit/swarm.sock service ls
"

# Deploy a test service
vagrant ssh manager -c "
  sudo swarmctl -s /var/run/swarmkit/swarm.sock service create \\
    --name nginx \\
    --image nginx:alpine \\
    --replicas 3
"

# Check task status
vagrant ssh manager -c "
  sudo swarmctl -s /var/run/swarmkit/swarm.sock task ls
"

# Check microVMs on workers
vagrant ssh worker1 -c "
  sudo swarmcracker list
"
```

## 💰 Cost Management

### Hourly Costs (Approximate)
- Manager (s-4vcpu-8gb): **$0.07/hour** (~$48/month)
- Worker (s-2vcpu-4gb): **$0.036/hour** (~$24/month)
- **Total 2-node cluster**: **$0.10/hour** (~$72/month)

### Money-Saving Tips

1. **Destroy when not testing:**
   ```bash
   vagrant destroy -f
   ```
   This immediately stops billing.

2. **Use smaller sizes for basic testing:**
   ```ruby
   # In Vagrantfile
   provider.size = 's-2vcpu-2gb'  # Only $0.022/hour
   ```

3. **Test with 1 node initially:**
   ```bash
   vagrant up manager
   # Test manager functionality first
   ```

4. **Set up cost alerts:**
   - Go to https://cloud.digitalocean.com/settings/billing
   - Set email alerts at $50, $100, etc.

## 🗂️ Switching Back to VirtualBox

Need to switch back to local VirtualBox testing?

```bash
# Restore original Vagrantfile
cp Vagrantfile.virtualbox.bak Vagrantfile

# Destroy DigitalOcean droplets first!
vagrant destroy -f

# Start VirtualBox VMs
vagrant up
```

## 🌍 Available Regions

Choose regions close to you for better latency:

| Region | Location | Latency to Jakarta |
|--------|----------|-------------------|
| sgp1   | Singapore | ~20ms ⭐ RECOMMENDED |
| syd1   | Sydney | ~100ms |
| nrt1   | Tokyo | ~50ms |

Edit `Vagrantfile` to change:
```ruby
provider.region = 'sgp1'  # Singapore
```

## 📊 Available Droplet Sizes

| Slug | vCPUs | RAM | Disk | Cost/month |
|------|-------|-----|------|-----------|
| s-1vcpu-1gb | 1 | 1GB | 25GB SSD | $6 |
| s-1vcpu-2gb | 1 | 2GB | 50GB SSD | $12 |
| s-2vcpu-2gb | 2 | 2GB | 60GB SSD | $24 |
| s-2vcpu-4gb | 2 | 4GB | 80GB SSD | $36 ⭐ Worker |
| s-4vcpu-8gb | 4 | 8GB | 160GB SSD | $72 ⭐ Manager |

## 🐛 Troubleshooting

### Plugin Installation Fails
```bash
# Manually install
vagrant plugin install vagrant-digitalocean

# If that fails, install dependencies
sudo apt-get install -y ruby-dev build-essential
vagrant plugin install vagrant-digitalocean
```

### "Token not found" Error
```bash
# Verify token is set
echo $DIGITAL_OCEAN_TOKEN

# Set it again
export DIGITAL_OCEAN_TOKEN=your_actual_token
```

### SSH Connection Issues
```bash
# Check your SSH key is added to DigitalOcean
# Go to: https://cloud.digitalocean.com/settings/security

# Or use password auth (not recommended)
# In Vagrantfile, add:
# provider.ssh_key_name = nil
```

### Droplet Stuck in "Creating" State
```bash
# Check status on DigitalOcean control panel
# https://cloud.digitalocean.com/droplets

# If stuck > 10 minutes, destroy and retry:
vagrant destroy worker1 -f
vagrant up worker1
```

## 📚 Next Steps

Once your cluster is running:

1. **Run automated tests:**
   ```bash
   ./test-deployment.sh
   ```

2. **Test manual deployments:**
   ```bash
   vagrant ssh manager
   sudo swarmctl -s /var/run/swarmkit/swarm.sock service create --name redis --image redis:alpine --replicas 2
   ```

3. **Monitor resource usage:**
   ```bash
   vagrant ssh worker1 -c "free -h && df -h"
   ```

## 🎓 Resources

- [Vagrant DigitalOcean Plugin](https://github.com/devops-art/digitalocean-vagrant)
- [DigitalOcean API Docs](https://docs.digitalocean.com/reference/api/api-reference/)
- [SwarmCracker Documentation](../README.md)

---

**Happy testing! 🚀**

Remember: `vagrant destroy -f` when done to stop billing!
