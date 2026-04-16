# Runbooks

Operational procedures for **{{PROJECT_NAME}}**.

## Purpose

Runbooks provide step-by-step procedures for common operational tasks and incident response.

## Categories

### Deployment

| Runbook | Description |
|---------|-------------|
| [Deploy to Production](#deploy-to-production) | Standard production deployment |
| [Rollback](#rollback) | How to rollback a bad deployment |

### Incident Response

| Runbook | Description |
|---------|-------------|
| [High CPU](#high-cpu) | Responding to high CPU alerts |
| [Database Issues](#database-issues) | Database troubleshooting |

---

## Deploy to Production

### Prerequisites

- [ ] All tests passing
- [ ] PR approved and merged to main
- [ ] Changelog updated

### Steps

1. **Verify CI/CD Pipeline**
   ```bash
   # Check pipeline status
   gh run list --limit 5
   ```

2. **Deploy**
   ```bash
   # Trigger deployment
   # (Add your deployment command here)
   ```

3. **Verify Deployment**
   ```bash
   # Health check
   curl -s https://your-app.com/health
   ```

4. **Monitor**
   - Check application logs
   - Monitor error rates
   - Verify metrics

---

## Rollback

### When to Rollback

- Critical functionality broken
- Error rate exceeds threshold
- Performance degradation

### Steps

1. **Identify Last Good Version**
   ```bash
   git log --oneline -10
   ```

2. **Execute Rollback**
   ```bash
   # (Add your rollback command here)
   ```

3. **Verify Rollback**
   - Confirm previous version is running
   - Check error rates returning to normal

4. **Post-Mortem**
   - Document what went wrong
   - Create ADR if architectural changes needed

---

## High CPU

### Symptoms

- CPU usage > 80% sustained
- Alerts from monitoring

### Investigation

1. **Check Current Load**
   ```bash
   # View top processes
   top -b -n 1 | head -20
   ```

2. **Check Application Logs**
   ```bash
   # Recent errors
   tail -100 /var/log/app/error.log
   ```

3. **Check for Runaway Processes**
   ```bash
   ps aux --sort=-%cpu | head -10
   ```

### Resolution

- Scale horizontally if traffic-related
- Restart service if memory leak suspected
- Investigate code if specific endpoint

---

## Database Issues

### Connection Issues

```bash
# Test database connection
psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "SELECT 1"
```

### Slow Queries

```sql
-- Find slow queries (PostgreSQL)
SELECT query, calls, mean_time
FROM pg_stat_statements
ORDER BY mean_time DESC
LIMIT 10;
```

### Disk Space

```bash
# Check database size
psql -c "SELECT pg_size_pretty(pg_database_size('dbname'));"
```

---

*Keep runbooks up-to-date as procedures evolve.*
