# Email Deliverability Engine — implemented in OutReachCRM

This spec is built into the Go app as `internal/deliverability`, not Django/Celery/Postgres.

## Where it lives

| Spec layer | App module |
|------------|------------|
| Architecture (CRM → Engine → SMTP) | `sequencing.Worker` calls `deliverability.Engine.Evaluate` before send |
| 1 Validation | `validate.go` + curated disposable list |
| 2 SMTP verify | `smtpverify.go` (off by default: `DELIVERABILITY_SMTP_VERIFY`) |
| 3 Domain reputation | `score.go` `ScoreDomain` |
| 4 Recipient history | `recipient_stats` table + `GetRecipientHistory` |
| 5 Bounce prediction | Heuristic `PredictBounce` (no XGBoost — keeps ≤30 MB binary) |
| 6 Spam-trap risk | `SpamTrapRisk` |
| 7 Engagement | `PredictEngagement` |
| 8 Send-time | `sendtime.go` (night hours only) |
| 9 Warm-up | `WarmupDailyLimit` → `mail.EffectiveDailyQuota` |
| 10 ISP throttle | `isp.go` + `isp_send_log` |
| 11 Complaints | Webhooks → stats → `PauseHotCampaigns` |
| 12 Blacklists | DNSBL via `/deliverability` UI |
| 13 Auth | Reuses `dnscheck` SPF/DKIM/DMARC |
| 14 Content | `content.go` |
| 15 Dashboard | `/deliverability` |
| 16 Auto-suppress | Hard bounce / complaint / disposable / high risk |

## UI

- **Deliverability** nav → reputation dashboard, validate email, DNSBL, decision log
- Leads → **Validate email**
- Campaigns → deliverability pause / clear

## Env

```
DELIVERABILITY_SMTP_VERIFY=false
DELIVERABILITY_BLACKLIST_CHECK=true
DELIVERABILITY_REQUIRE_AUTH=false
DELIVERABILITY_OPTIMIZE_SEND_TIME=true
```
