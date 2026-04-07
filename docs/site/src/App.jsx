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
    { icon: '🔥', title: 'MicroVM Isolation', description: 'Each container gets its own kernel via KVM, providing hardware-level security and strong workload isolation.' },
    { icon: '🐳', title: 'SwarmKit Orchestration', description: 'Services, scaling, rolling updates, secrets management - all the features you expect from modern orchestration.' },
    { icon: '⚡', title: 'Fast Startup', description: 'MicroVMs boot in milliseconds with minimal overhead, combining container speed with VM security.' },
    { icon: '🛡️', title: 'Hardware Security', description: 'KVM virtualization provides stronger isolation than container namespaces, protecting against kernel exploits.' },
    { icon: '🌐', title: 'VXLAN Networking', description: 'Cross-node VM communication with VXLAN overlay networks, supporting multi-node clusters out of the box.' },
    { icon: '🔄', title: 'Rolling Updates', description: 'Zero-downtime deployments with health monitoring and automatic rollback on failure.' }
  ]

  const stats = [
    { value: '<100ms', label: 'MicroVM Boot Time' },
    { value: '<5MB', label: 'Memory Overhead' },
    { value: '100%', label: 'KVM Isolation' },
    { value: 'Linux', label: 'Native Support' }
  ]

  const steps = [
    { number: 1, title: 'Initialize Cluster', description: 'Run swarmcracker init to set up the manager node with automatic token generation.' },
    { number: 2, title: 'Join Workers', description: 'Worker nodes join the cluster using the provided token, forming a SwarmKit cluster.' },
    { number: 3, title: 'Deploy Services', description: 'Deploy container images that automatically run as isolated Firecracker microVMs.' },
    { number: 4, title: 'Scale & Manage', description: 'Scale services, perform rolling updates, and monitor health through SwarmKit.' }
  ]

  const copyToClipboard = (text) => {
    navigator.clipboard.writeText(text)
  }

  const styles = {
    nav: { position: 'fixed', top: 0, width: '100%', background: 'rgba(10, 14, 26, 0.95)', backdropFilter: 'blur(10px)', borderBottom: '1px solid var(--border)', zIndex: 1000, padding: '1rem 0' },
    container: { maxWidth: '1200px', margin: '0 auto', padding: '0 2rem' },
    navContent: { display: 'flex', justifyContent: 'space-between', alignItems: 'center' },
    logo: { fontSize: '1.5rem', fontWeight: 700, display: 'flex', alignItems: 'center', gap: '0.5rem', color: 'white' },
    navLinks: { display: 'flex', gap: '2rem', listStyle: 'none', alignItems: 'center' },
    navLink: { color: 'var(--text-secondary)', transition: 'color 0.3s' },
    btnGh: { background: 'linear-gradient(135deg, #FF6B35 0%, #FF8E53 100%)', color: 'white', padding: '0.5rem 1.5rem', borderRadius: '8px', fontWeight: 600 },
    hero: { paddingTop: '10rem', paddingBottom: '6rem', textAlign: 'center', position: 'relative', overflow: 'hidden' },
    heroTitle: { fontSize: '3.5rem', fontWeight: 800, marginBottom: '1.5rem', background: 'linear-gradient(135deg, #FF6B35 0%, #FF8E53 100%)', WebkitBackgroundClip: 'text', WebkitTextFillColor: 'transparent', backgroundClip: 'text' },
    heroDesc: { fontSize: '1.25rem', color: 'var(--text-secondary)', maxWidth: '700px', margin: '0 auto 3rem' },
    heroButtons: { display: 'flex', gap: '1rem', justifyContent: 'center', flexWrap: 'wrap', marginBottom: '4rem' },
    btn: { display: 'inline-block', padding: '1rem 2rem', borderRadius: '12px', fontWeight: 600, transition: 'all 0.3s' },
    btnPrimary: { background: 'linear-gradient(135deg, #FF6B35 0%, #FF8E53 100%)', color: 'white', boxShadow: '0 4px 20px rgba(255, 107, 53, 0.3)' },
    btnSecondary: { background: 'var(--bg-card)', color: 'var(--text-primary)', border: '1px solid var(--border)' },
    codeBox: { background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: '12px', padding: 0, maxWidth: '800px', margin: '0 auto', overflow: 'hidden' },
    codeHeader: { background: 'var(--bg-dark)', padding: '0.75rem 1.5rem', borderBottom: '1px solid var(--border)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' },
    codeDots: { display: 'flex', gap: '0.5rem' },
    codeDot: { width: '12px', height: '12px', borderRadius: '50%' },
    codeContent: { padding: '1.5rem 2rem', overflowX: 'auto', fontFamily: "'JetBrains Mono', monospace", fontSize: '0.9rem', lineHeight: 1.7 },
    section: { padding: '6rem 0' },
    sectionBg: { padding: '6rem 0', background: 'var(--bg-card)' },
    sectionHeader: { textAlign: 'center', marginBottom: '4rem' },
    sectionTitle: { fontSize: '2.5rem', marginBottom: '1rem' },
    sectionDesc: { color: 'var(--text-secondary)', fontSize: '1.1rem' },
    featuresGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(300px, 1fr))', gap: '2rem' },
    featureCard: { background: 'var(--bg-dark)', padding: '2rem', borderRadius: '12px', border: '1px solid var(--border)', transition: 'all 0.3s' },
    featureIcon: { fontSize: '2.5rem', marginBottom: '1rem' },
    featureTitle: { fontSize: '1.25rem', marginBottom: '0.75rem' },
    featureDesc: { color: 'var(--text-secondary)' },
    statsGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: '2rem', textAlign: 'center' },
    statValue: { fontSize: '3rem', background: 'linear-gradient(135deg, #FF6B35 0%, #FF8E53 100%)', WebkitBackgroundClip: 'text', WebkitTextFillColor: 'transparent', backgroundClip: 'text', marginBottom: '0.5rem' },
    statLabel: { color: 'var(--text-secondary)' },
    stepsGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(250px, 1fr))', gap: '3rem', marginTop: '3rem' },
    step: { textAlign: 'center' },
    stepNumber: { width: '60px', height: '60px', background: 'linear-gradient(135deg, #FF6B35 0%, #FF8E53 100%)', borderRadius: '50%', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: '1.5rem', fontWeight: 700, margin: '0 auto 1.5rem' },
    installTabs: { display: 'flex', gap: '1rem', marginBottom: '2rem', flexWrap: 'wrap' },
    tabBtn: (active) => ({ padding: '0.75rem 1.5rem', background: active ? 'linear-gradient(135deg, #FF6B35 0%, #FF8E53 100%)' : 'var(--bg-dark)', border: '1px solid var(--border)', borderRadius: '8px', color: active ? 'white' : 'var(--text-secondary)', cursor: 'pointer', fontWeight: 500, transition: 'all 0.3s' }),
    installCode: { background: 'var(--bg-dark)', border: '1px solid var(--border)', borderRadius: '12px', padding: 0, overflow: 'hidden' },
    cta: { padding: '6rem 0' },
    ctaBox: { background: 'linear-gradient(135deg, #FF6B35 0%, #FF8E53 100%)', padding: '4rem 2rem', borderRadius: '20px', maxWidth: '900px', margin: '0 auto', textAlign: 'center' },
    ctaTitle: { fontSize: '2.5rem', marginBottom: '1rem' },
    ctaDesc: { fontSize: '1.1rem', marginBottom: '2rem', opacity: 0.95 },
    ctaBtn: { background: 'white', color: 'var(--primary)' },
    footer: { background: 'var(--bg-card)', padding: '3rem 0', borderTop: '1px solid var(--border)' },
    footerContent: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: '2rem', marginBottom: '2rem' },
    footerSection: {},
    footerTitle: { marginBottom: '1rem' },
    footerList: { listStyle: 'none' },
    footerListItem: { marginBottom: '0.5rem' },
    footerBottom: { textAlign: 'center', paddingTop: '2rem', borderTop: '1px solid var(--border)', color: 'var(--text-secondary)' }
  }

  return (
    <div style={{ minHeight: '100vh', background: 'var(--bg-dark)', color: 'var(--text-primary)' }}>
      {/* Navigation */}
      <nav style={styles.nav}>
        <div style={styles.container}>
          <div style={styles.navContent}>
            <a href="#" style={styles.logo}><span style={{ fontSize: '1.8rem' }}>🔥</span>SwarmCracker</a>
            <ul style={styles.navLinks}>
              <li><a href="#features" style={styles.navLink}>Features</a></li>
              <li><a href="#how-it-works" style={styles.navLink}>How It Works</a></li>
              <li><a href="#installation" style={styles.navLink}>Installation</a></li>
              <li><a href="https://github.com/restuhaqza/SwarmCracker/tree/main/docs" target="_blank" rel="noopener noreferrer" style={styles.navLink}>Docs</a></li>
              <li><a href="https://github.com/restuhaqza/SwarmCracker" target="_blank" rel="noopener noreferrer" style={styles.btnGh}>GitHub</a></li>
            </ul>
          </div>
        </div>
      </nav>

      {/* Hero Section */}
      <section style={styles.hero}>
        <div style={styles.container}>
          <h1 style={styles.heroTitle}>Firecracker MicroVMs with<br />SwarmKit Orchestration</h1>
          <p style={styles.heroDesc}>Run containers as isolated microVMs with hardware-level security, fast startup, and production-ready orchestration features.</p>
          <div style={styles.heroButtons}>
            <a href="#installation" style={{ ...styles.btn, ...styles.btnPrimary }}>Get Started</a>
            <a href="https://github.com/restuhaqza/SwarmCracker" target="_blank" rel="noopener noreferrer" style={{ ...styles.btn, ...styles.btnSecondary }}>View on GitHub</a>
          </div>
          <div style={styles.codeBox}>
            <div style={styles.codeHeader}>
              <div style={styles.codeDots}>
                <span style={{ ...styles.codeDot, background: '#FF5F56' }} />
                <span style={{ ...styles.codeDot, background: '#FFBD2E' }} />
                <span style={{ ...styles.codeDot, background: '#27C93F' }} />
              </div>
              <span style={{ color: 'var(--text-secondary)', fontFamily: "'JetBrains Mono', monospace", fontSize: '0.85rem' }}>bash</span>
            </div>
            <div style={styles.codeContent} className="code-syntax">
              <span className="comment"># Initialize a cluster in one command</span><br />
              <span className="command">curl</span> <span className="flag">-fsSL</span> <span className="string">https://swarmcracker.restuhaqza.dev/install.sh</span> | <span className="command">sudo</span> <span className="command">bash</span> <span className="flag">-s</span> <span className="flag">--</span> <span className="command">init</span><br /><br />
              <span className="comment"># Join workers to the cluster</span><br />
              <span className="command">swarmcracker</span> <span className="command">join</span> <span className="string">&lt;manager-ip&gt;:4242</span> <span className="flag">--token</span> <span className="string">SWMTKN-1-...</span>
            </div>
          </div>
        </div>
      </section>

      {/* Features Section */}
      <section id="features" style={styles.sectionBg}>
        <div style={styles.container}>
          <div style={styles.sectionHeader}>
            <h2 style={styles.sectionTitle}>Why SwarmCracker?</h2>
            <p style={styles.sectionDesc}>Production-grade container orchestration with microVM isolation</p>
          </div>
          <div style={styles.featuresGrid}>
            {features.map((feature, index) => (
              <div key={index} style={styles.featureCard}>
                <div style={styles.featureIcon}>{feature.icon}</div>
                <h3 style={styles.featureTitle}>{feature.title}</h3>
                <p style={styles.featureDesc}>{feature.description}</p>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* Stats Section */}
      <section style={styles.section}>
        <div style={styles.container}>
          <div style={styles.statsGrid}>
            {stats.map((stat, index) => (
              <div key={index}>
                <div style={styles.statValue}>{stat.value}</div>
                <div style={styles.statLabel}>{stat.label}</div>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* How It Works */}
      <section id="how-it-works" style={styles.section}>
        <div style={styles.container}>
          <div style={styles.sectionHeader}>
            <h2 style={styles.sectionTitle}>How It Works</h2>
            <p style={styles.sectionDesc}>From container image to isolated microVM in seconds</p>
          </div>
          <div style={styles.stepsGrid}>
            {steps.map((step) => (
              <div key={step.number} style={styles.step}>
                <div style={styles.stepNumber}>{step.number}</div>
                <h3 style={styles.featureTitle}>{step.title}</h3>
                <p style={styles.featureDesc}>{step.description}</p>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* Installation Section */}
      <section id="installation" style={styles.sectionBg}>
        <div style={styles.container}>
          <div style={styles.sectionHeader}>
            <h2 style={styles.sectionTitle}>Installation</h2>
            <p style={styles.sectionDesc}>Get started in minutes with our automated installer</p>
          </div>
          <div style={styles.installTabs}>
            {['manager', 'worker', 'manual'].map((tab) => (
              <button
                key={tab}
                onClick={() => setActiveTab(tab)}
                style={styles.tabBtn(activeTab === tab)}
              >
                {tab.charAt(0).toUpperCase() + tab.slice(1)} Node
              </button>
            ))}
          </div>
          <div style={styles.installCode}>
            <div style={styles.codeHeader}>
              <div style={styles.codeDots}>
                <span style={{ ...styles.codeDot, background: '#FF5F56' }} />
                <span style={{ ...styles.codeDot, background: '#FFBD2E' }} />
                <span style={{ ...styles.codeDot, background: '#27C93F' }} />
              </div>
              <span style={{ color: 'var(--text-secondary)', fontFamily: "'JetBrains Mono', monospace", fontSize: '0.85rem', textTransform: 'capitalize' }}>{activeTab}</span>
              <button onClick={() => copyToClipboard(installCommands[activeTab])} style={{ background: 'transparent', border: 'none', color: 'var(--text-secondary)', cursor: 'pointer', fontSize: '0.85rem' }}>Copy</button>
            </div>
            <div style={styles.codeContent} className="code-syntax">
              <code>{installCommands[activeTab]}</code>
            </div>
          </div>
        </div>
      </section>

      {/* CTA Section */}
      <section style={styles.cta}>
        <div style={styles.container}>
          <div style={styles.ctaBox}>
            <h2 style={styles.ctaTitle}>Ready to Get Started?</h2>
            <p style={styles.ctaDesc}>Join the growing community of developers using SwarmCracker for secure, isolated container orchestration.</p>
            <a href="https://github.com/restuhaqza/SwarmCracker" target="_blank" rel="noopener noreferrer" style={{ ...styles.btn, ...styles.ctaBtn }}>View on GitHub</a>
          </div>
        </div>
      </section>

      {/* Footer */}
      <footer style={styles.footer}>
        <div style={styles.container}>
          <div style={styles.footerContent}>
            <div style={styles.footerSection}>
              <h4 style={styles.footerTitle}>🔥 SwarmCracker</h4>
              <p style={{ color: 'var(--text-secondary)', fontSize: '0.9rem' }}>Firecracker MicroVMs with SwarmKit Orchestration</p>
            </div>
            <div style={styles.footerSection}>
              <h4 style={styles.footerTitle}>Documentation</h4>
              <ul style={styles.footerList}>
                <li style={styles.footerListItem}><a href="https://github.com/restuhaqza/SwarmCracker/tree/main/docs/getting-started" target="_blank" rel="noopener noreferrer">Getting Started</a></li>
                <li style={styles.footerListItem}><a href="https://github.com/restuhaqza/SwarmCracker/tree/main/docs/architecture" target="_blank" rel="noopener noreferrer">Architecture</a></li>
                <li style={styles.footerListItem}><a href="https://github.com/restuhaqza/SwarmCracker/tree/main/docs/guides" target="_blank" rel="noopener noreferrer">Guides</a></li>
                <li style={styles.footerListItem}><a href="https://github.com/restuhaqza/SwarmCracker/releases" target="_blank" rel="noopener noreferrer">Releases</a></li>
              </ul>
            </div>
            <div style={styles.footerSection}>
              <h4 style={styles.footerTitle}>Community</h4>
              <ul style={styles.footerList}>
                <li style={styles.footerListItem}><a href="https://github.com/restuhaqza/SwarmCracker" target="_blank" rel="noopener noreferrer">GitHub</a></li>
                <li style={styles.footerListItem}><a href="https://github.com/restuhaqza/SwarmCracker/issues" target="_blank" rel="noopener noreferrer">Issues</a></li>
                <li style={styles.footerListItem}><a href="https://github.com/restuhaqza/SwarmCracker/discussions" target="_blank" rel="noopener noreferrer">Discussions</a></li>
              </ul>
            </div>
            <div style={styles.footerSection}>
              <h4 style={styles.footerTitle}>Resources</h4>
              <ul style={styles.footerList}>
                <li style={styles.footerListItem}><a href="https://firecracker-microvm.github.io/" target="_blank" rel="noopener noreferrer">Firecracker</a></li>
                <li style={styles.footerListItem}><a href="https://github.com/moby/swarmkit" target="_blank" rel="noopener noreferrer">SwarmKit</a></li>
                <li style={styles.footerListItem}><a href="https://www.linux-kvm.org/" target="_blank" rel="noopener noreferrer">KVM</a></li>
              </ul>
            </div>
          </div>
          <div style={styles.footerBottom}>
            <p>&copy; 2026 SwarmCracker. Apache 2.0 Licensed.</p>
          </div>
        </div>
      </footer>
    </div>
  )
}

export default App
