#!/bin/bash
# setup-digitalocean.sh - Quick setup for DigitalOcean deployment

set -e

echo "🌊 Setting up SwarmCracker on DigitalOcean..."
echo ""

# Check required environment variables
if [ -z "$DIGITAL_OCEAN_TOKEN" ]; then
  echo "❌ ERROR: DIGITAL_OCEAN_TOKEN environment variable not set"
  echo ""
  echo "Please set your DigitalOcean API token:"
  echo "  export DIGITAL_OCEAN_TOKEN=your_token_here"
  echo ""
  echo "Get your token from: https://cloud.digitalocean.com/settings/api/tokens"
  exit 1
fi

# Optional SSH key name
if [ -z "$DIGITAL_OCEAN_SSH_KEY" ]; then
  echo "⚠️  WARNING: DIGITAL_OCEAN_SSH_KEY not set (using 'vagrant' default)"
  echo "   Set it with: export DIGITAL_OCEAN_SSH_KEY=your_key_name"
  echo ""
fi

echo "✅ DigitalOcean token found"
echo ""

# Check if vagrant-digitalocean plugin is installed
if ! vagrant plugin list | grep -q "vagrant-digitalocean"; then
  echo "📦 Installing vagrant-digitalocean plugin..."
  vagrant plugin install vagrant-digitalocean
  echo ""
fi

# Create backup of original Vagrantfile
if [ -f "Vagrantfile" ] && [ ! -f "Vagrantfile.virtualbox.bak" ]; then
  echo "💾 Backing up original Vagrantfile..."
  cp Vagrantfile Vagrantfile.virtualbox.bak
fi

# Use DigitalOcean Vagrantfile
echo "🔄 Switching to DigitalOcean configuration..."
cp Vagrantfile.digitalocean Vagrantfile

echo ""
echo "🚀 Ready to deploy!"
echo ""
echo "Next steps:"
echo "  1. Review Vagrantfile if needed (region, sizes, etc.)"
echo "  2. Start the cluster:"
echo "     vagrant up"
echo ""
echo "💰 Estimated cost:"
echo "  - Manager (s-4vcpu-8gb):  ~\$0.07/hour (\$48/month)"
echo "  - Worker  (s-2vcpu-4gb):  ~\$0.036/hour (\$24/month)"
echo "  - Total:                  ~\$0.10/hour (\$72/month)"
echo ""
echo "⏹️  To destroy droplets when done:"
echo "     vagrant destroy -f"
echo ""
