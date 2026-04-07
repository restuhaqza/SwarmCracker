import { useState } from 'react'

function App() {
  const [activeTab, setActiveTab] = useState('manager')

  const installCommands = {
    manager: `# Initialize a new SwarmCracker cluster
curl -fsSL https://swarmcracker.restuhaqza.dev/install.sh | sudo bash -s -- init

# With VXLAN overlay (multi-node)
curl -fsSL https://swarmcracker.restuhaqza.dev/install.sh | sudo bash -s -- init \\
  --vxlan-enabled \\
  --vxlan-peers 192.168.1.11,192.168.1.12

# Get join token for workers
sudo cat /var/lib/swarmkit/join-tokens.txt`,
    worker: `# Join an existing cluster
curl -fsSL https://swarmcracker.restuhaqza.dev/install.sh | sudo bash -s -- join \\
  --manager <MANAGER_IP>:4242 \\
  --token SWMTKN-1-...

# With VXLAN overlay
curl -fsSL https://swarmcracker.restuhaqza.dev/install.sh | sudo bash -s -- join \\
  --manager <MANAGER_IP>:4242 \\
  --token SWMTKN-1-... \\
  --vxlan-enabled \\
  --vxlan-peers <PEER_IPS>`,
    manual: `# Download installer
wget https://swarmcracker.restuhaqza.dev/install.sh
chmod +x install.sh

# Or download from releases
wget https://github.com/restuhaqza/SwarmCracker/releases/latest/download/swarmcracker-linux-amd64.tar.gz
tar -xzf swarmcracker-linux-amd64.tar.gz
sudo cp swarmcracker-*-linux-amd64/* /usr/local/bin/

# Verify installation
swarmcracker version

# Initialize cluster
sudo swarmcracker init`
  }

  const features = [
    {
      icon: '🔥',
      title: 'MicroVM Isolation',
      description: 'Each container gets its own kernel via KVM, providing hardware-level security and strong workload isolation.'
    },
    {
      icon: '🐳',
      title: 'SwarmKit Orchestration',
      description: 'Services, scaling, rolling updates, secrets management - all the features you expect from modern orchestration.'
    },
    {
      icon: '⚡',
      title: 'Fast Startup',
      description: 'MicroVMs boot in milliseconds with minimal overhead, combining container speed with VM security.'
    },
    {
      icon: '🛡️',
      title: 'Hardware Security',
      description: 'KVM virtualization provides stronger isolation than container namespaces, protecting against kernel exploits.'
    },
    {
      icon: '🌐',
      title: 'VXLAN Networking',
      description: 'Cross-node VM communication with VXLAN overlay networks, supporting multi-node clusters out of the box.'
    },
    {
      icon: '🔄',
      title: 'Rolling Updates',
      description: 'Zero-downtime deployments with health monitoring and automatic rollback on failure.'
    }
  ]

  const stats = [
    { value: '<100ms', label: 'MicroVM Boot Time' },
    { value: '<5MB', label: 'Memory Overhead' },
    { value: '100%', label: 'KVM Isolation' },
    { value: 'Linux', label: 'Native Support' }
  ]

  const steps = [
    {
      number: 1,
      title: 'Initialize Cluster',
      description: 'Run swarmcracker init to set up the manager node with automatic token generation.'
    },
    {
      number: 2,
      title: 'Join Workers',
      description: 'Worker nodes join the cluster using the provided token, forming a SwarmKit cluster.'
    },
    {
      number: 3,
      title: 'Deploy Services',
      description: 'Deploy container images that automatically run as isolated Firecracker microVMs.'
    },
    {
      number: 4,
      title: 'Scale & Manage',
      description: 'Scale services, perform rolling updates, and monitor health through SwarmKit.'
    }
  ]

  const copyToClipboard = (text) => {
    navigator.clipboard.writeText(text)
    // Could add toast notification here
  }

  return (
    <div className="min-h-screen bg-bg-dark text-white">
      {/* Navigation */}
      <nav className="fixed top-0 w-full bg-bg-dark/95 backdrop-blur-sm border-b border-border z-50">
        <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
          <div className="flex justify-between items-center h-16">
            <a href="#" className="flex items-center gap-2 text-xl font-bold">
              <span className="text-2xl">🔥</span>
              SwarmCracker
            </a>
            <div className="hidden md:flex items-center gap-8">
              <a href="#features" className="text-text-secondary hover:text-primary transition-colors">Features</a>
              <a href="#how-it-works" className="text-text-secondary hover:text-primary transition-colors">How It Works</a>
              <a href="#installation" className="text-text-secondary hover:text-primary transition-colors">Installation</a>
              <a href="https://github.com/restuhaqza/SwarmCracker/tree/main/docs" target="_blank" rel="noopener noreferrer" className="text-text-secondary hover:text-primary transition-colors">Docs</a>
              <a href="https://github.com/restuhaqza/SwarmCracker" target="_blank" rel="noopener noreferrer" className="bg-gradient px-4 py-2 rounded-lg font-semibold hover:opacity-90 transition-opacity">GitHub</a>
            </div>
          </div>
        </div>
      </nav>

      {/* Hero Section */}
      <section className="pt-32 pb-20 px-4 relative overflow-hidden">
        <div className="absolute top-0 left-1/2 -translate-x-1/2 w-[1000px] h-[1000px] bg-gradient-radial from-primary/10 to-transparent pointer-events-none" />
        
        <div className="max-w-7xl mx-auto text-center relative z-10">
          <h1 className="text-5xl md:text-6xl font-extrabold mb-6 text-gradient">
            Firecracker MicroVMs with<br />SwarmKit Orchestration
          </h1>
          <p className="text-xl text-text-secondary max-w-3xl mx-auto mb-10">
            Run containers as isolated microVMs with hardware-level security, fast startup, and production-ready orchestration features.
          </p>
          
          <div className="flex gap-4 justify-center flex-wrap mb-12">
            <a href="#installation" className="bg-gradient text-white px-8 py-4 rounded-xl font-semibold shadow-lg shadow-primary/30 hover:shadow-primary/40 hover:-translate-y-0.5 transition-all">
              Get Started
            </a>
            <a href="https://github.com/restuhaqza/SwarmCracker" target="_blank" rel="noopener noreferrer" className="bg-bg-card border border-border px-8 py-4 rounded-xl font-semibold hover:border-primary transition-colors">
              View on GitHub
            </a>
          </div>

          {/* Hero Code Block */}
          <div className="max-w-3xl mx-auto bg-bg-card border border-border rounded-2xl overflow-hidden">
            <div className="bg-bg-dark px-4 py-3 border-b border-border flex justify-between items-center">
              <div className="flex gap-2">
                <div className="w-3 h-3 rounded-full bg-[#FF5F56]" />
                <div className="w-3 h-3 rounded-full bg-[#FFBD2E]" />
                <div className="w-3 h-3 rounded-full bg-[#27C93F]" />
              </div>
              <span className="text-sm text-text-secondary font-mono">bash</span>
            </div>
            <pre className="p-6 overflow-x-auto text-left font-mono text-sm code-syntax">
              <code>{installCommands.manager}</code>
            </pre>
          </div>
        </div>
      </section>

      {/* Features Section */}
      <section id="features" className="py-20 px-4 bg-bg-card">
        <div className="max-w-7xl mx-auto">
          <div className="text-center mb-16">
            <h2 className="text-4xl font-bold mb-4">Why SwarmCracker?</h2>
            <p className="text-text-secondary text-lg">Production-grade container orchestration with microVM isolation</p>
          </div>
          
          <div className="grid md:grid-cols-2 lg:grid-cols-3 gap-6">
            {features.map((feature, index) => (
              <div key={index} className="bg-bg-dark p-6 rounded-xl border border-border hover:border-primary hover:-translate-y-1 transition-all">
                <div className="text-4xl mb-4">{feature.icon}</div>
                <h3 className="text-xl font-semibold mb-2">{feature.title}</h3>
                <p className="text-text-secondary">{feature.description}</p>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* Stats Section */}
      <section className="py-16 px-4">
        <div className="max-w-7xl mx-auto">
          <div className="grid grid-cols-2 md:grid-cols-4 gap-8">
            {stats.map((stat, index) => (
              <div key={index} className="text-center">
                <div className="text-4xl md:text-5xl font-bold text-gradient mb-2">{stat.value}</div>
                <div className="text-text-secondary">{stat.label}</div>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* How It Works */}
      <section id="how-it-works" className="py-20 px-4">
        <div className="max-w-7xl mx-auto">
          <div className="text-center mb-16">
            <h2 className="text-4xl font-bold mb-4">How It Works</h2>
            <p className="text-text-secondary text-lg">From container image to isolated microVM in seconds</p>
          </div>
          
          <div className="grid md:grid-cols-4 gap-8">
            {steps.map((step) => (
              <div key={step.number} className="text-center">
                <div className="w-16 h-16 bg-gradient rounded-full flex items-center justify-center text-2xl font-bold mx-auto mb-4">
                  {step.number}
                </div>
                <h3 className="text-xl font-semibold mb-2">{step.title}</h3>
                <p className="text-text-secondary">{step.description}</p>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* Installation Section */}
      <section id="installation" className="py-20 px-4 bg-bg-card">
        <div className="max-w-7xl mx-auto">
          <div className="text-center mb-16">
            <h2 className="text-4xl font-bold mb-4">Installation</h2>
            <p className="text-text-secondary text-lg">Get started in minutes with our automated installer</p>
          </div>

          {/* Tabs */}
          <div className="flex gap-2 mb-6 flex-wrap">
            {['manager', 'worker', 'manual'].map((tab) => (
              <button
                key={tab}
                onClick={() => setActiveTab(tab)}
                className={`px-6 py-3 rounded-lg font-medium transition-all ${
                  activeTab === tab
                    ? 'bg-gradient text-white'
                    : 'bg-bg-dark text-text-secondary hover:text-white'
                }`}
              >
                {tab.charAt(0).toUpperCase() + tab.slice(1)} Node
              </button>
            ))}
          </div>

          {/* Code Block */}
          <div className="bg-bg-dark border border-border rounded-2xl overflow-hidden">
            <div className="bg-bg-card px-4 py-3 border-b border-border flex justify-between items-center">
              <div className="flex gap-2">
                <div className="w-3 h-3 rounded-full bg-[#FF5F56]" />
                <div className="w-3 h-3 rounded-full bg-[#FFBD2E]" />
                <div className="w-3 h-3 rounded-full bg-[#27C93F]" />
              </div>
              <span className="text-sm text-text-secondary font-mono capitalize">{activeTab}</span>
              <button
                onClick={() => copyToClipboard(installCommands[activeTab])}
                className="flex items-center gap-2 text-text-secondary hover:text-primary transition-colors text-sm"
              >
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>
                  <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>
                </svg>
                Copy
              </button>
            </div>
            <pre className="p-6 overflow-x-auto text-left font-mono text-sm code-syntax">
              <code>{installCommands[activeTab]}</code>
            </pre>
          </div>
        </div>
      </section>

      {/* CTA Section */}
      <section className="py-20 px-4">
        <div className="max-w-4xl mx-auto">
          <div className="bg-gradient rounded-3xl p-12 text-center">
            <h2 className="text-4xl font-bold mb-4">Ready to Get Started?</h2>
            <p className="text-lg mb-8 opacity-95">
              Join the growing community of developers using SwarmCracker for secure, isolated container orchestration.
            </p>
            <a
              href="https://github.com/restuhaqza/SwarmCracker"
              target="_blank"
              rel="noopener noreferrer"
              className="inline-block bg-white text-primary px-8 py-4 rounded-xl font-semibold hover:bg-bg-dark hover:text-white transition-colors"
            >
              View on GitHub
            </a>
          </div>
        </div>
      </section>

      {/* Footer */}
      <footer className="py-12 px-4 bg-bg-card border-t border-border">
        <div className="max-w-7xl mx-auto">
          <div className="grid md:grid-cols-4 gap-8 mb-8">
            <div>
              <h4 className="font-semibold mb-4 flex items-center gap-2">
                <span>🔥</span>
                SwarmCracker
              </h4>
              <p className="text-text-secondary text-sm">
                Firecracker MicroVMs with SwarmKit Orchestration
              </p>
            </div>
            
            <div>
              <h4 className="font-semibold mb-4">Documentation</h4>
              <ul className="space-y-2 text-sm">
                <li><a href="https://github.com/restuhaqza/SwarmCracker/tree/main/docs/getting-started" target="_blank" rel="noopener noreferrer" className="text-text-secondary hover:text-primary transition-colors">Getting Started</a></li>
                <li><a href="https://github.com/restuhaqza/SwarmCracker/tree/main/docs/architecture" target="_blank" rel="noopener noreferrer" className="text-text-secondary hover:text-primary transition-colors">Architecture</a></li>
                <li><a href="https://github.com/restuhaqza/SwarmCracker/tree/main/docs/guides" target="_blank" rel="noopener noreferrer" className="text-text-secondary hover:text-primary transition-colors">Guides</a></li>
                <li><a href="https://github.com/restuhaqza/SwarmCracker/releases" target="_blank" rel="noopener noreferrer" className="text-text-secondary hover:text-primary transition-colors">Releases</a></li>
              </ul>
            </div>
            
            <div>
              <h4 className="font-semibold mb-4">Community</h4>
              <ul className="space-y-2 text-sm">
                <li><a href="https://github.com/restuhaqza/SwarmCracker" target="_blank" rel="noopener noreferrer" className="text-text-secondary hover:text-primary transition-colors">GitHub</a></li>
                <li><a href="https://github.com/restuhaqza/SwarmCracker/issues" target="_blank" rel="noopener noreferrer" className="text-text-secondary hover:text-primary transition-colors">Issues</a></li>
                <li><a href="https://github.com/restuhaqza/SwarmCracker/discussions" target="_blank" rel="noopener noreferrer" className="text-text-secondary hover:text-primary transition-colors">Discussions</a></li>
              </ul>
            </div>
            
            <div>
              <h4 className="font-semibold mb-4">Resources</h4>
              <ul className="space-y-2 text-sm">
                <li><a href="https://firecracker-microvm.github.io/" target="_blank" rel="noopener noreferrer" className="text-text-secondary hover:text-primary transition-colors">Firecracker</a></li>
                <li><a href="https://github.com/moby/swarmkit" target="_blank" rel="noopener noreferrer" className="text-text-secondary hover:text-primary transition-colors">SwarmKit</a></li>
                <li><a href="https://www.linux-kvm.org/" target="_blank" rel="noopener noreferrer" className="text-text-secondary hover:text-primary transition-colors">KVM</a></li>
              </ul>
            </div>
          </div>
          
          <div className="pt-8 border-t border-border text-center text-text-secondary text-sm">
            <p>&copy; 2026 SwarmCracker. Apache 2.0 Licensed.</p>
          </div>
        </div>
      </footer>
    </div>
  )
}

export default App
