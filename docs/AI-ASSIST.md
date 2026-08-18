# Staff AI, mailbox sync, and marketing SMTP

## Staff AI

Dashboard and inbox have a compact assistant (`POST /api/dashboard/ai/chat`, `POST /api/inbox/ai/chat`).

The model can call read tools (`workspace_pulse`, `search_leads`, `get_lead`, `list_campaigns`, `list_queue`, `list_inbox_replies`) and confirm-gated writes (`update_lead_status`, `enroll_in_campaign`, `draft_reply`, `create_followup_note`). Writes return `needs_confirmation` until `confirm=true` (or `AutoConfirm` in code).

Prompts and an optional OpenAI key override live per workspace on `/settings/email`. Env fallback: `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL`.

`OUTREACH_AI_MODE`:

| Value | Staff chat | Inbound after IMAP classify |
|-------|------------|-----------------------------|
| `off` (default) | Hidden | No drafts |
| `suggest` | On (if a key is set) | Save a HITL draft via SuggestReply |
| `auto` | On | Same drafts; unsubscribe gets a confirmation draft only — no free-form auto-send |

## Mailbox vs paid ESP

| Connection | Where | Used for |
|------------|--------|----------|
| Gmail / Outlook OAuth, or Titan / Zoho / Hostinger / custom IMAP | `/accounts` | Inbox sync + 1:1 HITL replies |
| Marketing SMTP (Brevo, SendGrid, Mailgun, Postmark, SES, or generic SMTP) | `/settings/email` | Campaign / bulk outbound |

When marketing SMTP is configured, the sequencer never uses personal Gmail/Outlook for blasts. Bounce webhooks remain Postmark/SES only; other ESPs: use their dashboard + our suppressions.

## Configure

```bash
export OPENAI_API_KEY=sk-...
export OUTREACH_AI_MODE=suggest
# Then in the app: /settings/email → from address + ESP API key or SMTP password
```
