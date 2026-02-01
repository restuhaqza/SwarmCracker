# 🚀 Quick Start Guide - SwarmCracker Test Cluster

## 📍 Location
```
/home/kali/.openclaw/workspace/projects/swarmcracker/test-automation/
```

## ⚡ 3 Steps to Start Testing

### 1️⃣ Install Prerequisites (one time)
```bash
sudo apt-get update
sudo apt-get install -y virtualbox vagrant
```

### 2️⃣ Start the Cluster (~10 minutes)
```bash
cd /home/kali/.openclaw/workspace/projects/swarmcracker/test-automation
./start-cluster.sh
```

### 3️⃣ Run Tests
```bash
./test-deployment.sh
```

---

## 🎯 What You Get

✅ **3 VMs** (1 manager + 2 workers)  
✅ **SwarmKit cluster** fully configured  
✅ **SwarmCracker executor** on workers  
✅ **Firecracker microVMs** ready to run  

---

## 🎮 Quick Commands

```bash
# Cluster status
vagrant ssh manager -c "export SWARM_SOCKET=/var/run/swarmkit/swarm.sock && swarmctl node ls"

# Deploy a service
vagrant ssh manager -c "export SWARM_SOCKET=/var/run/swarmkit/swarm.sock && swarmctl service create --name web --image nginx:alpine --replicas 3"

# Check services
vagrant ssh manager -c "export SWARM_SOCKET=/var/run/swarmkit/swarm.sock && swarmctl service ps web"

# List microVMs on worker
vagrant ssh worker1 -c "sudo swarmcracker list"

# Stop everything
./destroy-cluster.sh
```

---

## 📊 VM Details

| VM       | IP             | RAM   | CPUs | Role          |
|----------|----------------|-------|------|---------------|
| manager  | 192.168.56.10  | 2 GB  | 2    | SwarmKit mgr  |
| worker1  | 192.168.56.11  | 4 GB  | 4    + SwarmCracker |
| worker2  | 192.168.56.12  | 4 GB  | 4    + SwarmCracker |

---

## 🧪 Test Scenarios

The `test-deployment.sh` script runs:
- ✅ Service deployment (nginx)
- ✅ Scaling (3 → 5 replicas)
- ✅ Rolling updates
- ✅ Multi-service stack
- ✅ MicroVM verification

---

## 🐛 Troubleshooting

```bash
# Check VM status
vagrant status

# Restart a VM
vagrant reload worker1

# View manager logs
vagrant ssh manager -c "journalctl -u swarmd -f"

# View worker logs
vagrant ssh worker1 -c "journalctl -u swarmd -f"

# Reset everything
./destroy-cluster.sh && ./start-cluster.sh
```

---

## 📚 Full Documentation

See [README.md](README.md) for complete documentation.

---

**Ready?** Run: `./start-cluster.sh`
