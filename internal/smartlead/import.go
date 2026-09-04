package smartlead

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/models"
	"github.com/manishkumar/outreachcrm/internal/store"
)

const importPageYield = 20 * time.Millisecond

type Options struct {
	WorkspaceName string
	DryRun        bool
}

type Stats struct {
	WorkspaceID  int64
	OwnerID      int64
	Campaigns    int
	Steps        int
	Accounts     int
	Leads        int
	Enrollments  int
	Outbound     int
	Replies      int
	Suppressions int
	SkippedDupes int
	Events       int
	People       int
	Errors       []string
}

func (s *Stats) err(msg string) {
	s.Errors = append(s.Errors, msg)
	if len(s.Errors) > 50 {
		s.Errors = s.Errors[:50]
	}
}

func Run(st *store.Store, c *Client, opt Options) (Stats, error) {
	var out Stats
	wsName := strings.TrimSpace(opt.WorkspaceName)
	if wsName == "" {
		wsName = "Smartlead"
	}
	ownerID, err := st.FirstAdminID()
	if err != nil && !opt.DryRun {
		return out, fmt.Errorf("no admin user (bootstrap OutReachCRM first): %w", err)
	}
	out.OwnerID = ownerID
	if opt.DryRun {
		slog.Info("smartlead import dry-run: no writes")
	} else {
		st.SetBusyTimeout(30000)
		wsID, err := st.EnsureNamedWorkspace(wsName)
		if err != nil {
			return out, err
		}
		out.WorkspaceID = wsID
		st.Audit(wsID, ownerID, "smartlead.import.start", "workspace", strconv.FormatInt(wsID, 10), "")
	}

	accounts, err := c.ListEmailAccounts()
	if err != nil {
		return out, fmt.Errorf("email accounts: %w", err)
	}
	slog.Info("smartlead accounts fetched", "count", len(accounts))
	for _, a := range accounts {
		if opt.DryRun {
			out.Accounts++
			continue
		}
		remote := strconv.FormatInt(a.ID, 10)
		if a.ID == 0 {
			remote = strings.ToLower(a.Email)
		}
		if id, ok := st.SmartleadLookup("account", remote); ok && id > 0 {
			out.Accounts++
			continue
		}
		id, err := st.CreateAccount(models.EmailAccount{
			OwnerID: ownerID, WorkspaceID: out.WorkspaceID,
			Email: a.Email, Provider: models.ProviderSmartlead,
			Username: a.Email, DailyQuota: 0, WarmupEnabled: a.Warmup,
			Domain: domainOf(a.Email),
		})
		if err != nil {
			out.err("account " + a.Email + ": " + err.Error())
			continue
		}
		_ = st.SmartleadRemember("account", remote, id)
		out.Accounts++
	}

	camps, err := c.ListCampaigns()
	if err != nil {
		return out, fmt.Errorf("campaigns: %w", err)
	}
	slog.Info("smartlead campaigns", "count", len(camps))
	for i, camp := range camps {
		slog.Info("smartlead campaign", "i", i+1, "n", len(camps), "id", camp.ID, "name", camp.Name)
		if camp.ID == 0 {
			continue
		}
		detail, err := c.GetCampaign(camp.ID)
		if err != nil {
			slog.Warn("campaign detail", "id", camp.ID, "err", err)
			detail = camp
		}
		if detail.Name == "" {
			detail.Name = camp.Name
		}
		if detail.Name == "" {
			detail.Name = fmt.Sprintf("Smartlead %d", camp.ID)
		}

		var localCamp int64
		if opt.DryRun {
			out.Campaigns++
		} else {
			remote := strconv.FormatInt(camp.ID, 10)
			if id, ok := st.SmartleadLookup("campaign", remote); ok && id > 0 {
				localCamp = id
			} else {
				id, err := st.CreateCampaign(models.Campaign{
					OwnerID: ownerID, WorkspaceID: out.WorkspaceID,
					Name: detail.Name, Status: "paused",
					DailySendLimit:  detail.DailySendLimit,
					Timezone:        detail.Timezone,
					SendWindowStart: detail.SendWindowStart,
					SendWindowEnd:   detail.SendWindowEnd,
				})
				if err != nil {
					out.err("campaign " + detail.Name + ": " + err.Error())
					continue
				}
				localCamp = id
				_ = st.SmartleadRemember("campaign", remote, id)
			}
			out.Campaigns++
			// Record whether this campaign measured opens and clicks. Most
			// disable both, and scoring must read that as "not measured"
			// rather than "not interested".
			if localCamp > 0 {
				if err := st.SetCampaignTracking(localCamp, detail.TrackOpens, detail.TrackClicks); err != nil {
					slog.Warn("campaign tracking", "id", localCamp, "err", err)
				}
			}
		}

		steps, err := c.ListSequences(camp.ID)
		if err != nil {
			out.err(fmt.Sprintf("sequences campaign %d: %v", camp.ID, err))
			steps = nil
		}
		if !opt.DryRun && localCamp > 0 {
			n, _ := st.CountCampaignSteps(localCamp)
			if n == 0 {
				for i, stStep := range steps {
					order := stStep.Order
					if order <= 0 {
						order = i + 1
					}
					if _, err := st.AddStep(models.SequenceStep{
						CampaignID: localCamp, StepOrder: order, DelayDays: stStep.DelayDays,
						SubjectTemplate: stStep.Subject, BodySpintax: stStep.Body,
						VariantBSubject: stStep.VariantBSubject, VariantBBody: stStep.VariantBBody,
					}); err != nil {
						out.err("step: " + err.Error())
						continue
					}
					out.Steps++
				}
			} else {
				out.Steps += n
			}
		} else {
			out.Steps += len(steps)
		}

		leadByEmail := map[string]Lead{}
		localLead := map[string]int64{}
		var leadCount int
		err = c.EachCampaignLeads(camp.ID, "", func(page []Lead, meta PageMeta) error {
			for _, ld := range page {
				email := strings.ToLower(strings.TrimSpace(ld.Email))
				if email == "" {
					continue
				}
				leadByEmail[email] = ld
				enroll, leadStatus, suppress, _ := MapEnrollment(ld.Status, ld.Unsubscribed, ld.ReplyCount)
				if opt.DryRun {
					out.Leads++
					out.Enrollments++
					if suppress != "" {
						out.Suppressions++
					}
					leadCount++
					continue
				}
				id, created, err := upsertLead(st, ownerID, out.WorkspaceID, ld, leadStatus)
				if err != nil {
					out.err("lead " + email + ": " + err.Error())
					continue
				}
				if created {
					out.Leads++
				} else {
					out.SkippedDupes++
				}
				localLead[email] = id
				step := ld.LastSeqSent
				clID, err := st.ImportCampaignLead(localCamp, id, step, enroll, "")
				if err != nil {
					out.err("enroll " + email + ": " + err.Error())
					continue
				}
				out.Enrollments++
				_ = st.SmartleadRemember("campaign_lead:"+strconv.FormatInt(camp.ID, 10), email, clID)
				if suppress != "" {
					if err := st.AddSuppressionWS(out.WorkspaceID, email, suppress); err != nil {
						out.err("suppress " + email + ": " + err.Error())
					} else {
						out.Suppressions++
					}
				}
				leadCount++
			}
			done := meta.Offset + len(page)
			if meta.Total > 0 {
				done = min(done, meta.Total)
			}
			slog.Info("smartlead campaign leads", "id", camp.ID, "done", done, "total_leads", meta.Total, "page", len(page))
			if !opt.DryRun {
				st.WALCheckpointTruncate()
			}
			yieldImport()
			return nil
		})
		if err != nil {
			out.err(fmt.Sprintf("leads campaign %d: %v", camp.ID, err))
		}
		slog.Info("smartlead campaign leads done", "id", camp.ID, "leads", leadCount)

		repliedEmails := map[string]bool{}
		var writes int
		crmSteps, _ := st.ListSteps(localCamp)
		var statsSeen int
		err = c.EachStatistics(camp.ID, func(page []StatRow, meta PageMeta) error {
			for _, row := range page {
				email := strings.ToLower(strings.TrimSpace(row.LeadEmail))
				if email == "" {
					continue
				}
				if row.Replied {
					repliedEmails[email] = true
				}
				if opt.DryRun {
					out.Outbound++
					statsSeen++
					continue
				}
				n, w := insertStatOutbound(st, &out, camp.ID, localCamp, email, row, localLead, leadByEmail, crmSteps)
				writes += w
				statsSeen += n
			}
			total := meta.Total
			slog.Info("smartlead stats progress", "campaign", camp.ID, "inserted", out.Outbound, "page_rows", len(page), "seen", statsSeen, "total_stats", total, "offset", meta.Offset)
			if !opt.DryRun {
				st.WALCheckpointTruncate()
			}
			yieldImport()
			return nil
		})
		if err != nil {
			out.err(fmt.Sprintf("statistics campaign %d: %v", camp.ID, err))
		}

		for _, ld := range leadByEmail {
			if !WantsReplyHistory(ld.Status, ld.ReplyCount) && !repliedEmails[strings.ToLower(ld.Email)] {
				continue
			}
			if ld.ID == 0 {
				continue
			}
			hist, err := c.MessageHistory(camp.ID, ld.ID)
			if err != nil {
				out.err(fmt.Sprintf("history %s: %v", ld.Email, err))
				continue
			}
			email := strings.ToLower(ld.Email)
			lid := localLead[email]
			if lid == 0 && !opt.DryRun {
				if existing, err := st.FindLeadByEmailInWorkspace(email, out.WorkspaceID); err == nil && existing.ID > 0 {
					lid = existing.ID
					localLead[email] = lid
				}
			}
			for _, msg := range hist {
				if opt.DryRun {
					if isInbound(msg.Direction) {
						out.Replies++
					} else {
						out.Outbound++
					}
					continue
				}
				if lid == 0 {
					continue
				}
				if isInbound(msg.Direction) {
					intent := "other"
					_, leadStatus, _, _ := MapEnrollment(ld.Status, ld.Unsubscribed, ld.ReplyCount)
					if leadStatus == models.LeadStatusInterested {
						intent = "positive"
					} else if leadStatus == models.LeadStatusNotInterested {
						intent = "negative"
					}
					mid := msg.ID
					if mid == "" {
						mid = fmt.Sprintf("sl-in-%d-%d-%s", camp.ID, ld.ID, msg.At)
					}
					ws := out.WorkspaceID
					oid := ownerID
					at := store.ParseImportTime(msg.At)
					_, err := st.CreateReply(models.InboundReply{
						OwnerID: &oid, WorkspaceID: &ws, LeadID: &lid,
						LeadName:  LeadDisplayName(ld.FirstName, ld.LastName, ld.Email),
						FromEmail: ld.Email, Subject: msg.Subject, Body: msg.Body,
						Intent: intent, MessageID: mid, HITLStatus: models.HITLAuto,
						CreatedAt: at,
					})
					if err != nil {
						if strings.Contains(err.Error(), "duplicate") {
							continue
						}
						out.err("reply " + email + ": " + err.Error())
						continue
					}
					out.Replies++
					writes++
					continue
				}
				clID, ok := lookupOrImportCampaignLead(st, camp.ID, localCamp, lid, email, leadByEmail)
				if !ok || clID == 0 {
					continue
				}
				step := 1
				if ld.LastSeqSent > 0 {
					step = ld.LastSeqSent
				}
				mid := msg.ID
				if mid == "" {
					mid = fmt.Sprintf("sl-out-%d-%d-%s", camp.ID, ld.ID, msg.At)
				}
				sentAt := store.ParseImportTime(msg.At)
				_, inserted, err := st.InsertHistoricalOutbound(models.OutboundMessage{
					CampaignID: localCamp, LeadID: lid, CampaignLeadID: clID, StepOrder: step,
					ToEmail: email, Subject: msg.Subject, Body: msg.Body, Status: "sent",
					ScheduledAt: sentAt, SentAt: timePtr(sentAt), MessageID: mid,
				})
				if err != nil {
					out.err("history outbound " + email + ": " + err.Error())
					continue
				}
				writes++
				if inserted {
					out.Outbound++
				}
			}
			if writes > 0 && writes%80 == 0 {
				st.WALCheckpointTruncate()
				slog.Info("smartlead history progress", "campaign", camp.ID, "writes", writes, "replies", out.Replies)
				yieldImport()
			}
		}
		if !opt.DryRun {
			st.WALCheckpointTruncate()
		}
	}

	blocked, err := c.ListBlockList()
	if err != nil {
		slog.Warn("block-list skipped", "err", err)
	} else if !opt.DryRun {
		for _, e := range blocked {
			e = strings.ToLower(strings.TrimSpace(e))
			if e == "" {
				continue
			}
			if err := st.AddSuppressionWS(out.WorkspaceID, e, "block_list"); err == nil {
				out.Suppressions++
			}
		}
	} else {
		out.Suppressions += len(blocked)
	}

	// Build the prospect-memory layer from whatever this run imported. It is
	// idempotent, so running it after every import simply tops up: new leads
	// become people, and people already present are left alone.
	if !opt.DryRun {
		res, err := st.BackfillProspects()
		if err != nil {
			out.err("prospect backfill: " + err.Error())
		} else {
			out.People = res.PeopleCreated
			slog.Info("prospect backfill",
				"people", res.PeopleCreated, "emails", res.EmailsCreated,
				"employment", res.EmploymentCreated,
				"messages_linked", res.MessagesLinked, "replies_linked", res.RepliesLinked)
		}
		if n, err := st.BackfillEventsFromMessages(out.WorkspaceID); err != nil {
			out.err("event backfill: " + err.Error())
		} else {
			out.Events = n
			slog.Info("prospect events", "created", n)
		}
	}

	if !opt.DryRun {
		st.Audit(out.WorkspaceID, ownerID, "smartlead.import.done", "workspace", strconv.FormatInt(out.WorkspaceID, 10),
			fmt.Sprintf("campaigns=%d leads=%d enroll=%d sent=%d replies=%d people=%d events=%d",
				out.Campaigns, out.Leads, out.Enrollments, out.Outbound, out.Replies, out.People, out.Events))
	}
	return out, nil
}

func yieldImport() {
	runtime.Gosched()
	time.Sleep(importPageYield)
}

func insertStatOutbound(st *store.Store, out *Stats, remoteCampID, localCamp int64, email string, row StatRow, localLead map[string]int64, leadByEmail map[string]Lead, crmSteps []models.SequenceStep) (seen, writes int) {
	seen = 1
	lid := localLead[email]
	if lid == 0 {
		if existing, err := st.FindLeadByEmailInWorkspace(email, out.WorkspaceID); err == nil && existing.ID > 0 {
			lid = existing.ID
			localLead[email] = lid
		}
	}
	if lid == 0 {
		return seen, 0
	}
	clID, ok := lookupOrImportCampaignLead(st, remoteCampID, localCamp, lid, email, leadByEmail)
	if !ok || clID == 0 {
		return seen, 0
	}
	step := row.SeqNumber
	if step <= 0 {
		step = 1
	}
	sentAt := store.ParseImportTime(row.SentAt)
	status := "sent"
	if row.Bounced {
		status = "dead"
	}
	msgID := row.StatsID
	if msgID == "" {
		msgID = fmt.Sprintf("sl-%d-%s-%d", remoteCampID, email, step)
	}
	subj, body := row.EmailSubject, row.EmailMessage
	if subj == "" || body == "" {
		for _, sstep := range crmSteps {
			if sstep.StepOrder == step {
				if subj == "" {
					subj = sstep.SubjectTemplate
				}
				if body == "" {
					body = sstep.BodySpintax
				}
				break
			}
		}
	}
	_, inserted, err := st.InsertHistoricalOutbound(models.OutboundMessage{
		CampaignID: localCamp, LeadID: lid, CampaignLeadID: clID, StepOrder: step,
		ToEmail: email, Subject: subj, Body: body, Status: status,
		ScheduledAt: sentAt, SentAt: timePtr(sentAt), MessageID: msgID,
		Opened: row.Opened, Replied: row.Replied,
		LastError: bounceErr(row.Bounced),
	})
	if err != nil {
		out.err("outbound " + email + ": " + err.Error())
		return seen, 0
	}
	writes = 1
	if inserted {
		out.Outbound++
		_ = st.RecordRecipientEvent(out.WorkspaceID, email, "sent")
		if row.Opened {
			_ = st.RecordRecipientEvent(out.WorkspaceID, email, "opened")
		}
		if row.Clicked {
			_ = st.RecordRecipientEvent(out.WorkspaceID, email, "clicked")
		}
		if row.Replied {
			_ = st.RecordRecipientEvent(out.WorkspaceID, email, "replied")
		}
		if row.Bounced {
			_ = st.RecordRecipientEvent(out.WorkspaceID, email, "hard_bounce")
		}
		if row.Unsubscribed {
			_ = st.AddSuppressionWS(out.WorkspaceID, email, "unsubscribed")
		}
	}
	return seen, writes
}

func lookupOrImportCampaignLead(st *store.Store, remoteCampID, localCamp, lid int64, email string, leadByEmail map[string]Lead) (int64, bool) {
	if localCamp <= 0 || lid <= 0 {
		return 0, false
	}
	clRemote := "campaign_lead:" + strconv.FormatInt(remoteCampID, 10)
	if clID, ok := st.SmartleadLookup(clRemote, email); ok && clID > 0 {
		return clID, true
	}
	enroll := "completed"
	step := 0
	if ld, ok := leadByEmail[email]; ok {
		enroll, _, _, _ = MapEnrollment(ld.Status, ld.Unsubscribed, ld.ReplyCount)
		step = ld.LastSeqSent
	}
	clID, err := st.ImportCampaignLead(localCamp, lid, step, enroll, "")
	if err != nil || clID == 0 {
		return 0, false
	}
	_ = st.SmartleadRemember(clRemote, email, clID)
	return clID, true
}

func upsertLead(st *store.Store, ownerID, wsID int64, ld Lead, leadStatus string) (id int64, created bool, err error) {
	email := strings.TrimSpace(ld.Email)
	existing, findErr := st.FindLeadByEmailInWorkspace(email, wsID)
	if findErr == nil && existing.ID > 0 {
		_ = st.UpdateLeadStatus(existing.ID, leadStatus)
		return existing.ID, false, nil
	}
	notes := ""
	if ld.CustomJSON != "" && ld.CustomJSON != "null" && ld.CustomJSON != "{}" {
		var pretty any
		if json.Unmarshal([]byte(ld.CustomJSON), &pretty) == nil {
			b, _ := json.Marshal(pretty)
			notes = "smartlead custom_fields: " + string(b)
		} else {
			notes = "smartlead custom_fields: " + ld.CustomJSON
		}
	}
	id, err = st.CreateLead(models.Lead{
		OwnerID: ownerID, WorkspaceID: wsID,
		Name:  LeadDisplayName(ld.FirstName, ld.LastName, email),
		Email: email, Phone: ld.Phone, Website: ld.Website,
		Company: ld.Company, Title: ld.Title, Notes: notes,
		Source: "smartlead", EnrichmentStatus: "pending",
	})
	if err != nil {
		if existing, e2 := st.FindLeadByEmailInWorkspace(email, wsID); e2 == nil && existing.ID > 0 {
			return existing.ID, false, nil
		}
		return 0, false, err
	}
	_ = st.UpdateLeadStatus(id, leadStatus)
	return id, true, nil
}

func domainOf(email string) string {
	i := strings.LastIndex(email, "@")
	if i < 0 {
		return ""
	}
	return strings.ToLower(email[i+1:])
}

func isInbound(dir string) bool {
	d := strings.ToLower(dir)
	return d == "inbound" || d == "in" || d == "reply" || d == "received"
}

func bounceErr(bounced bool) string {
	if bounced {
		return "bounced"
	}
	return ""
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
