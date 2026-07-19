package store

import (
	"fmt"
	"strings"

	"github.com/manishkumar/outreachcrm/internal/models"
)

// SeedCompanyPlaybooks loads all company playbooks into one workspace (legacy).
func (s *Store) SeedCompanyPlaybooks(ownerID, workspaceID int64) (purged, templates, campaigns int, err error) {
	return s.SeedCompanyPlaybooksFor(ownerID, workspaceID, PlaybookAll)
}

// SeedCompanyPlaybooksFor loads OVM and/or RevNext playbooks into workspaceID.
// company: ovm | revnext | all
func (s *Store) SeedCompanyPlaybooksFor(ownerID, workspaceID int64, company string) (purged, templates, campaigns int, err error) {
	if workspaceID == 0 {
		workspaceID = 1
	}
	company = strings.ToLower(strings.TrimSpace(company))
	if company == "" {
		company = PlaybookAll
	}

	purged, _ = s.PurgeDummyLeads()

	for _, t := range filterTemplatesByCompany(companyTemplates(workspaceID), company) {
		if s.templateExists(workspaceID, t.Name) {
			continue
		}
		if _, e := s.CreateTemplate(t); e == nil {
			templates++
		}
	}

	for _, pack := range filterCampaignsByCompany(companyCampaignPacks(ownerID, workspaceID), company) {
		if s.campaignExists(workspaceID, pack.Campaign.Name) {
			continue
		}
		id, e := s.CreateCampaign(pack.Campaign)
		if e != nil {
			return purged, templates, campaigns, e
		}
		for i, st := range pack.Steps {
			st.CampaignID = id
			st.StepOrder = i + 1
			if _, e := s.AddStep(st); e != nil {
				return purged, templates, campaigns, e
			}
		}
		campaigns++
	}
	return purged, templates, campaigns, nil
}

func filterTemplatesByCompany(all []models.EmailTemplate, company string) []models.EmailTemplate {
	if company == PlaybookAll {
		return all
	}
	prefix := "OVM ·"
	if company == PlaybookRevNext {
		prefix = "RevNext ·"
	} else if company != PlaybookOVM {
		return all
	}
	var out []models.EmailTemplate
	for _, t := range all {
		if strings.HasPrefix(t.Name, prefix) {
			out = append(out, t)
		}
	}
	return out
}

func filterCampaignsByCompany(all []campaignPack, company string) []campaignPack {
	if company == PlaybookAll {
		return all
	}
	prefix := "OVM ·"
	if company == PlaybookRevNext {
		prefix = "RevNext ·"
	} else if company != PlaybookOVM {
		return all
	}
	var out []campaignPack
	for _, p := range all {
		if strings.HasPrefix(p.Campaign.Name, prefix) {
			out = append(out, p)
		}
	}
	return out
}

// PurgeDummyLeads hard-deletes seeded demo / example.* ICP rows (and related outbound).
func (s *Store) PurgeDummyLeads() (int, error) {
	where := `lower(email) LIKE '%@example.com'
		OR lower(email) LIKE '%@example.org'
		OR lower(email) LIKE '%@example.net'
		OR lower(email) LIKE '%.example.com'
		OR lower(email) LIKE '%.example.org'
		OR lower(email) LIKE '%.example.net'
		OR source = 'seed'`
	_, _ = s.db.Exec(`DELETE FROM outbound_messages WHERE lead_id IN (SELECT id FROM leads WHERE ` + where + `)`)
	res, err := s.db.Exec(`DELETE FROM leads WHERE ` + where)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) templateExists(workspaceID int64, name string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM email_templates WHERE workspace_id=? AND name=?`, workspaceID, name).Scan(&n)
	return n > 0
}

func (s *Store) campaignExists(workspaceID int64, name string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM campaigns WHERE workspace_id=? AND name=?`, workspaceID, name).Scan(&n)
	return n > 0
}

type campaignPack struct {
	Campaign models.Campaign
	Steps    []models.SequenceStep
}

func campBase(ownerID, workspaceID int64, name string, daily int) models.Campaign {
	if daily <= 0 {
		daily = 35
	}
	return models.Campaign{
		OwnerID: ownerID, WorkspaceID: workspaceID, Name: name,
		Status: "draft", DailySendLimit: daily,
		Timezone: "Asia/Kolkata", SendWindowStart: 9, SendWindowEnd: 18,
		ABEnabled: true,
	}
}

func companyTemplates(workspaceID int64) []models.EmailTemplate {
	mk := func(name, subject, body string) models.EmailTemplate {
		return models.EmailTemplate{WorkspaceID: workspaceID, Name: name, Subject: subject, Body: body}
	}
	return []models.EmailTemplate{
		mk("OVM · MVP opener (fixed price)",
			"{Quick idea|Thought} for {{name}} — {MVP in 7–21 days|fixed-price MVP}",
			`Hi {{name}},

{I help founders|We help founders at OctaVertex Media} ship production MVPs on {fixed price + fixed timeline|a number you know before kickoff} — web, mobile, AI, or SaaS — with {100% code ownership|repos + docs you own}.

Packages (USD): Discovery $2,000 / 7 days · Starter $3,999 / 21 days · Rush $5,499 / 14 days.
→ https://www.octavertexmedia.com/build-mvp

{Most teams|Usually founders} waste months on agencies ($50k–$150k+) or freelancers. {We cap|Studio cap}: 4 MVPs / month so delivery stays sharp.

{Open to a free strategy call?|Worth a 15-min scope call?}
Manish · OctaVertex Media`),

		mk("OVM · Discovery Sprint $2k",
			"{{name}} — {go / no-go|validate} before you build",
			`Hi {{name}},

{Before you spend on engineering|Before writing production code}: map flows, stack, and a go/no-go — in 7 days.

OctaVertex Discovery Sprint: $2,000 · user flows · tech spec · interactive prototype · architecture · market-fit notes · 30-day Launch & Scale included.
https://www.octavertexmedia.com/build-mvp

{Want this for {{name}}'s idea?|Open to a short strategy call?}
Manish`),

		mk("OVM · Starter MVP $3999",
			"{{name}} — Starter MVP {21 days · $3,999|fixed scope}",
			`Hi {{name}},

{Most founders|Founders we work with} pick Starter: up to 5 core features, auth, UI/UX, API/DB, deploy, A/B hooks, 7-day post-launch + 30-day Launch & Scale — $3,999 / 21 days.

https://www.octavertexmedia.com/build-mvp

{Still scoping your v1?|Want a feature-fit check on a call?}
Manish · OctaVertex Media`),

		mk("OVM · Rush MVP $5499",
			"{{name}} — {pitch / launch deadline|14-day Rush MVP}",
			`Hi {{name}},

{If the calendar is brutal|When you need speed}: Rush MVP — everything in Starter + Stripe, admin, email, DB ready for 10x, 14-day post-launch — $5,499 / 14 days.

https://www.octavertexmedia.com/build-mvp

{Racing a demo or fundraise?|Worth locking a Rush slot?}
Manish`),

		mk("OVM · AI product opener",
			"{{name}} — {AI feature|LLM workflow} without the science project",
			`Hi {{name}},

{AI demos are cheap|Prototypes are easy}. {Shippable AI is hard|Production AI with cost + guardrails is hard}.

OctaVertex AI MVP track: LLM features / agents with monitoring — fixed scope on the build-mvp packages when it fits, or scoped from services.
https://www.octavertexmedia.com/build-mvp · https://www.octavertexmedia.com/services

{Want a 15-min architecture sketch?|Open to a short AI tear-down?}
Manish`),

		mk("OVM · E-com / Marketplace",
			"{{name}} — {storefront that converts|marketplace MVP} not a template",
			`Hi {{name}},

{Generic Shopify themes|Brochure storefronts} don't fix checkout or two-sided liquidity.

OctaVertex: e-commerce + marketplace builds (onboarding, payouts, admin) with conversion-first UX — then paid/SEO after launch if you need growth.
https://www.octavertexmedia.com/services

{Open to a scope call?|Want a 15-min funnel review?}
Manish`),

		mk("OVM · Post-MVP growth",
			"{{name}} — product live? {now grow CAC|fix the funnel}",
			`Hi {{name}},

{Once the MVP ships|After launch}: we run growth tied to product metrics — paid, SEO/content, landing copy — not vanity brand work. Retainer typically ~$2,999/mo (40 hrs), pause anytime.

https://www.octavertexmedia.com/services

{Useful this quarter?|Want a quick growth stack chat?}
Manish · OctaVertex Media`),

		mk("RevNext · Revenue audit",
			"{{name}} — {RevPAR leak|OTA commission} check for your property",
			`Hi {{name}},

{Quick question|Curious}: are {OTA commissions|parity + pricing gaps} quietly eating ADR/RevPAR?

RevNext runs a {weekly revenue system|pricing + OTA + direct booking rhythm} for hotels — {100+ properties|proven across 100+ stays}, ~35% avg revenue growth, clear owner reporting.

{Happy to|I can} send a {free revenue audit|45-min action plan} focused on your property.
→ https://revnext.in

{Worth 15 minutes this week?|Open to a short audit call?}
RevNext team`),

		mk("RevNext · Cloud PMS trial",
			"{{name}} — front desk that {doesn't lose the plot|stays in sync}",
			`Hi {{name}},

{If arrivals / HK / folios|When housekeeping and folios} live in different tools, the desk slows down.

RevNext Cloud PMS: arrivals board, HK status, GST-ready folios, linked rooms, multi-property login.
{14-day trial, no card|Start free — no card}.
→ https://pms.revnext.in

{Want a walkthrough for your property?|Open to a 10-min product tour?}
RevNext`),

		mk("RevNext · Cloud POS (F&B)",
			"{{name}} — {QR + waiter + room bill|outlet billing} in one POS",
			`Hi {{name}},

{Dining floors|F&B teams} lose minutes when {aggregator orders|Swiggy/Zomato + QR + waiters} aren't on the same bill.

RevNext Cloud POS: dine-in, takeaway, delivery, QR ordering, inventory, bill-to-room via PMS.
Trial: https://pos.revnext.in

{Worth a look for your outlet?|Happy to demo on your menu flow.}
RevNext`),

		mk("RevNext · Booking engine",
			"Cut OTA commission — {direct bookings|own your booking flow}",
			`Hi {{name}},

{Every direct booking|Each booking on your site} you win back is {commission you keep|margin that stays with you}.

RevNext Booking Engine: mobile-first direct booking, live ARI, Razorpay/payments, Google Hotel Ads feeds.
→ https://booking.revnext.in

{Want a 14-day trial on your site?|Open to embedding a trial widget?}
RevNext`),

		mk("RevNext · B2B network",
			"{{name}} — contract rates without {spreadsheet chaos|PDF rate wars}",
			`Hi {{name}},

{Travel agents / corporates|B2B buyers} on WhatsApp + PDFs = {leaks and disputes|rate leakage}.

RevNext Stay B2B: secure portals, contract rates, allotments, commissions — built for hotels + agencies.
→ https://networks.revnext.in

{Curious for your partner stack?|Want a short B2B walkthrough?}
RevNext`),

		mk("RevNext · Hotels listing",
			"List {{name}} on RevNext Hotels + metasearch path",
			`Hi {{name}},

{More distribution|Extra demand} without giving up your direct story.

RevNext Hotels: property discovery, claim listing, OTA setup tools, metasearch/Google Hotel feeds when linked to Booking Engine.
→ https://hotels.revnext.in

{Want help claiming your listing?|Open to a listing + booking stack chat?}
RevNext`),

		// —— OVM Manufacturing / B2B (octavertex-growth kit) ——
		mk("OVM · Mfg Lead Platform opener",
			"{{company}} catalogue → quote flow",
			`Hi {{name}},

{I work with|We help} manufacturing and B2B companies that need more than a brochure site—
typically a catalogue, quote/enquiry path, and basic CRM so leads don't die in WhatsApp.

Recently we shipped a full platform for a Mumbai packaging supplier (CMS, QR catalogue,
quote cart, SEO, production deploy).

I took a look at {{website}} — {worth tightening the enquiry path|catalogue/quote flow looks thin}.

Worth a 20-minute call this week to see if a Lead Generation Platform (usually ₹1.25L–₹2L,
fixed scope) is relevant?

https://www.octavertexmedia.com
Manish · Octavertex Media`),

		mk("OVM · Mfg case-led opener",
			"How a packaging supplier runs catalogue + WhatsApp quotes online",
			`Hi {{name}},

Sharing a pattern we see with {manufacturing|packaging|B2B} companies:

1. Buyers search Google / ask for catalogue on WhatsApp
2. Sales sends PDFs or photos
3. Follow-ups get lost—no CRM

We built a system for Intellect Kraft-class packaging sales: live catalogue, mobile QR
catalogue, quote cart, and CRM—plus SEO foundations.

If {{company}} is planning a digital upgrade this quarter, happy to walk through what
a scoped Lead Generation Platform looks like (architecture + ₹1.25L–₹2L investment bands).

{Open this week?|Worth a 20-min call?}
Manish · Octavertex Media · octavertexmedia.com`),

		mk("OVM · Mfg follow-up Day 2",
			"Re: Lead Generation Platform for {{company}}",
			`Hi {{name}} — bumping this once. Happy to send a one-page outline of the Lead Generation
Platform (deliverables + timeline) if easier than a call.

Manish · Octavertex Media`),

		mk("OVM · Mfg follow-up Day 5",
			"{{name}} — timing on website / catalogue?",
			`{{name}} — last note from me on this thread.

If website/catalogue isn't a priority this quarter, totally fine—I'll check back next
quarter. If it is, reply and we'll book 20 minutes.

Manish · Octavertex Media`),

		mk("OVM · Mfg follow-up Day 10",
			"{{company}} — 5 SEO page ideas for manufacturers",
			`{{name}} — related to {{company}}: many buyers search "{product|category} {city|India}".
If useful, I can send 5 page ideas that usually help manufacturers show up for those queries.

Manish · Octavertex Media`),

		mk("OVM · Mfg follow-up Day 21",
			"Honest teardown of {{website}}?",
			`Circling back one last time. If you want an honest teardown of {{website}} (15 min),
I'm happy to do it with no obligation.

Manish · Octavertex Media · octavertexmedia.com`),

		mk("OVM · Closing loop",
			"{Closing the loop|Last note} — {{name}}",
			`Hi {{name}},

{I'll close the loop here|Last note from me} on the MVP/build help.

Packages stay published: https://www.octavertexmedia.com/build-mvp
Services: https://www.octavertexmedia.com/services

{Happy to scope in one free strategy call|One short call is enough to size Discovery vs Starter vs Rush}.

{All the best|Rooting for the launch},
Manish · OctaVertex Media`),

		mk("RevNext · Closing loop",
			"{Closing the loop|Last note} — {{name}}",
			`Hi {{name}},

{I'll stop chasing|Last note} on the {revenue / product|RevNext} thread.

{Free audit + trials stay open|Audit + 14-day trials stay available}: https://revnext.in · https://pms.revnext.in

{Reply anytime if useful.|Happy to pick this up later.}
RevNext`),
	}
}

func companyCampaignPacks(ownerID, workspaceID int64) []campaignPack {
	return []campaignPack{
		// octavertex-growth: ≥4 clients/mo · manufacturing & B2B India · ₹1.25L+ Lead Platform
		{
			Campaign: campBase(ownerID, workspaceID, "OVM · Manufacturing Lead Platform (₹1.25L+)", 20),
			Steps: []models.SequenceStep{
				{
					DelayDays:       0,
					SubjectTemplate: "{{company}} catalogue → quote flow",
					BodySpintax: `Hi {{name}},

{I work with|We help} manufacturing and B2B companies that need more than a brochure site—
typically a catalogue, quote/enquiry path, and basic CRM so leads don't die in WhatsApp.

Recently we shipped a full platform for a Mumbai packaging supplier (CMS, QR catalogue,
quote cart, SEO, production deploy).

I took a look at {{website}} — {worth tightening the enquiry path|catalogue/quote flow looks thin}.

Worth a 20-minute call this week to see if a Lead Generation Platform (usually ₹1.25L–₹2L,
fixed scope) is relevant?

https://www.octavertexmedia.com
Manish · Octavertex Media`,
					VariantBSubject: "How a packaging supplier runs catalogue + WhatsApp quotes online",
					VariantBBody: `Hi {{name}},

Sharing a pattern we see with {manufacturing|packaging|B2B} companies:

1. Buyers search Google / ask for catalogue on WhatsApp
2. Sales sends PDFs or photos
3. Follow-ups get lost—no CRM

We built a system for Intellect Kraft-class packaging sales: live catalogue, mobile QR
catalogue, quote cart, and CRM—plus SEO foundations.

If {{company}} is planning a digital upgrade this quarter, happy to walk through a scoped
Lead Generation Platform (architecture + ₹1.25L–₹2L bands).

{Open this week?|Worth a 20-min call?}
Manish · Octavertex Media`,
				},
				{
					// relative delays → absolute Day 2 · 5 · 10 · 21 from enrollment
					DelayDays:       2,
					SubjectTemplate: "Re: Lead Generation Platform for {{company}}",
					BodySpintax: `Hi {{name}} — bumping this once. Happy to send a one-page outline of the Lead Generation
Platform (deliverables + timeline) if easier than a call.

Manish · Octavertex Media`,
				},
				{
					DelayDays:       3,
					SubjectTemplate: "{{name}} — timing on website / catalogue?",
					BodySpintax: `{{name}} — last note from me on this thread.

If website/catalogue isn't a priority this quarter, totally fine—I'll check back next
quarter. If it is, reply and we'll book 20 minutes.

Manish · Octavertex Media`,
				},
				{
					DelayDays:       5,
					SubjectTemplate: "{{company}} — 5 SEO page ideas for manufacturers",
					BodySpintax: `{{name}} — related to {{company}}: many buyers search "{product|category} {city|India}".
If useful, I can send 5 page ideas that usually help manufacturers show up for those queries.

Manish · Octavertex Media`,
				},
				{
					DelayDays:       11,
					SubjectTemplate: "Honest teardown of {{website}}?",
					BodySpintax: `Circling back one last time. If you want an honest teardown of {{website}} (15 min),
I'm happy to do it with no obligation.

Manish · Octavertex Media · octavertexmedia.com`,
				},
			},
		},
		{
			Campaign: campBase(ownerID, workspaceID, "OVM · Fixed-Price MVP (Web/Mobile/SaaS)", 30),
			Steps: []models.SequenceStep{
				{
					DelayDays:       0,
					SubjectTemplate: "{Quick idea|Thought} for {{name}} — {MVP $3,999 / 21 days|fixed-price MVP}",
					BodySpintax: `Hi {{name}},

{Noticed you're building|Saw signals you're shipping} something that needs to be {live this month|in users' hands soon}.

OctaVertex Media = AI & SaaS product studio: {fixed price before kickoff|no surprise invoices}, daily staging, {100% code ownership|you own the repos}.

{Starter MVP|Most-picked}: $3,999 · 21 days · up to 5 features · auth · deploy · Launch & Scale.
{Rush|Need faster}: $5,499 · 14 days · Stripe + admin.
{Discovery|Not sure yet}: $2,000 · 7 days · go/no-go.

https://www.octavertexmedia.com/build-mvp

{Open to a free strategy call?|Worth 15 minutes on scope?}
Manish`,
					VariantBSubject: "{{name}} — agencies quote $50k+. Starter MVP is $3,999.",
					VariantBBody: `Hi {{name}},

Cost / calendar / ownership side-by-side vs agencies & freelancers:
https://www.octavertexmedia.com/build-mvp

{One studio|Design + eng + launch} · 45+ products shipped · 4 MVPs/month cap.

Manish · OctaVertex Media`,
				},
				{
					DelayDays:       2,
					SubjectTemplate: "Re: {which package|Starter vs Rush} for {{name}}",
					BodySpintax: `Hi {{name}},

{Simple rule|How we pick}:
• {Validate first|Idea still fuzzy} → Discovery ($2k / 7d)
• {Core v1|5 features or fewer} → Starter ($3,999 / 21d)
• {Pitch / launch deadline|Payment + admin needed fast} → Rush ($5,499 / 14d)

https://www.octavertexmedia.com/build-mvp

{Still useful?|Want me to map your must-haves to a tier?}
Manish`,
				},
				{
					DelayDays:       4,
					SubjectTemplate: "{Shark-Tank-ready speed|Scope → Design → Build → Launch} — {{name}}",
					BodySpintax: `Hi {{name}},

{Process|How we ship}: Scope → Figma approval → daily sprints on staging → deploy on your infra with docs.
{Proof|Social proof}: Offline by Happy Hour shipped before Shark Tank — 50k+ users.

{Open Thu/Fri for a strategy call?|Any slot this week?}
https://www.octavertexmedia.com/build-mvp
Manish`,
				},
				{
					DelayDays:       7,
					SubjectTemplate: "{Closing the loop|Last note} — {{name}} MVP",
					BodySpintax: `Hi {{name}},

{I'll close the loop|Last note}. Packages + call: https://www.octavertexmedia.com/build-mvp
Services menu: https://www.octavertexmedia.com/services

{All the best|Rooting for the launch},
Manish`,
				},
			},
		},
		{
			Campaign: campBase(ownerID, workspaceID, "OVM · Discovery Sprint (Go/No-Go)", 25),
			Steps: []models.SequenceStep{
				{
					DelayDays:       0,
					SubjectTemplate: "{{name}} — {$2k Discovery|7-day go/no-go} before you build",
					BodySpintax: `Hi {{name}},

{Building the wrong v1|Jumping into eng} is the expensive mistake.

Discovery Sprint ($2,000 / 7 days): flows · tech spec · interactive prototype · architecture · market-fit · go/no-go — plus 30-day Launch & Scale.
https://www.octavertexmedia.com/build-mvp

{Want this before you hire / start coding?|Open to a short strategy call?}
Manish · OctaVertex Media`,
					VariantBSubject: "Prototype + blueprint in 7 days — {{name}}",
					VariantBBody: `Hi {{name}},

Discovery Sprint: $2k · clear yes/no on build · you leave with a plan founders can fund or kill cleanly.
https://www.octavertexmedia.com/build-mvp

Manish`,
				},
				{
					DelayDays:       3,
					SubjectTemplate: "Re: Discovery for {{name}}",
					BodySpintax: `Hi {{name}},

{Output you keep|What you walk away with}: Figma-ready flows, stack recommendation, feature cut list, and a {go / no-go|clear build call}.

{Still deciding?|Want to book Discovery?}
Manish`,
				},
				{
					DelayDays:       6,
					SubjectTemplate: "{Closing the loop|Last note} — Discovery",
					BodySpintax: `Hi {{name}},

https://www.octavertexmedia.com/build-mvp — {reply if timing opens|here when you're ready}.

Manish`,
				},
			},
		},
		{
			Campaign: campBase(ownerID, workspaceID, "OVM · AI / LLM Product Build", 25),
			Steps: []models.SequenceStep{
				{
					DelayDays:       0,
					SubjectTemplate: "{{name}} — {AI that ships|LLM without the science project}",
					BodySpintax: `Hi {{name}},

{AI prototypes are easy|Demos are cheap}. {Production AI is hard|Shipping AI with cost + guardrails is hard}.

OctaVertex AI MVP track (web/mobile/SaaS packages when scoped): {assistants + workflows|LLM features} with monitoring — fixed price when it fits Starter/Rush.
https://www.octavertexmedia.com/build-mvp · https://www.octavertexmedia.com/services

{Want a 15-min architecture sketch?|Open to a short AI tear-down?}
Manish`,
					VariantBSubject: "Stop endless AI experiments — ship a scoped slice",
					VariantBBody: `Hi {{name}},

We ship AI product slices founders can hand to users — with guardrails + cost awareness.
https://www.octavertexmedia.com/services

Manish`,
				},
				{
					DelayDays:       3,
					SubjectTemplate: "Re: {AI slice|LLM scope}",
					BodySpintax: `Hi {{name}},

{Concrete slice|What this looks like}: auth + data + one high-value AI workflow + admin + deploy docs — often inside a {21-day Starter|14-day Rush} if scope is tight.

{Useful for your roadmap?|Still exploring AI this quarter?}
Manish`,
				},
				{
					DelayDays:       6,
					SubjectTemplate: "{Closing the loop|Last note} — AI build",
					BodySpintax: `Hi {{name}},

{Closing here|Last note}. https://www.octavertexmedia.com/build-mvp

Manish`,
				},
			},
		},
		{
			Campaign: campBase(ownerID, workspaceID, "OVM · E-Com / Marketplace MVP", 25),
			Steps: []models.SequenceStep{
				{
					DelayDays:       0,
					SubjectTemplate: "{{name}} — {checkout that converts|marketplace MVP} not a brochure",
					BodySpintax: `Hi {{name}},

{Store templates|Generic themes} don't fix conversion, catalog ops, or two-sided trust.

OctaVertex e-commerce & marketplace work: {Shopify/custom storefronts|conversion-first checkout} or two-sided platforms (onboarding, payouts, admin) — then growth after launch if needed.
https://www.octavertexmedia.com/services
MVP packaging when it fits: https://www.octavertexmedia.com/build-mvp

{Open to a 15-min scope call?|Want a funnel tear-down?}
Manish`,
					VariantBSubject: "D2C / marketplace founders — fixed-scope product studio",
					VariantBBody: `Hi {{name}},

We build the product first (web/mobile), then paid + SEO tied to CAC/activation — not vanity campaigns.
https://www.octavertexmedia.com/services

Manish · OctaVertex Media`,
				},
				{
					DelayDays:       3,
					SubjectTemplate: "Re: e-com/marketplace — {{name}}",
					BodySpintax: `Hi {{name}},

{Industries we ship in|Common fits}: D2C, retail, travel, education marketplaces — {liquidity + trust mechanics|payouts + onboarding} scoped upfront.

{Still exploring a build partner?|Want package fit (Starter vs custom)?}
Manish`,
				},
				{
					DelayDays:       6,
					SubjectTemplate: "{Closing the loop|Last note} — e-com build",
					BodySpintax: `Hi {{name}},

https://www.octavertexmedia.com/services · https://www.octavertexmedia.com/build-mvp

Manish`,
				},
			},
		},
		{
			Campaign: campBase(ownerID, workspaceID, "OVM · Post-MVP Growth (Paid/SEO)", 20),
			Steps: []models.SequenceStep{
				{
					DelayDays:       0,
					SubjectTemplate: "{{name}} — product live? {grow CAC|fix the funnel}",
					BodySpintax: `Hi {{name}},

{Shipping was step one|MVP done?}. {Growth should track product metrics|Next is CAC, activation, checkout — not brand fluff}.

OctaVertex Grow: digital marketing, SEO/content, PPC, product copy — typically ~$2,999/mo retainer (40 hrs), pause anytime. Best after a real product exists.
https://www.octavertexmedia.com/services

{Useful this quarter?|Want a quick growth-stack chat?}
Manish`,
					VariantBSubject: "Post-launch growth for founders — not vanity media",
					VariantBBody: `Hi {{name}},

Paid + landing + SEO tied to how your product converts.
https://www.octavertexmedia.com/services

Manish`,
				},
				{
					DelayDays:       3,
					SubjectTemplate: "Re: growth for {{name}}",
					BodySpintax: `Hi {{name}},

{What we won't do|Hard no}: {brand-only campaigns|spend without funnel}. {What we will|Hard yes}: experiments tied to activation / revenue.

{Still interested?|Want a 15-min audit of your funnel?}
Manish`,
				},
				{
					DelayDays:       6,
					SubjectTemplate: "{Closing the loop|Last note} — growth",
					BodySpintax: `Hi {{name}},

https://www.octavertexmedia.com/services — {ping when you're post-MVP|here after launch}.

Manish`,
				},
			},
		},
		{
			Campaign: campBase(ownerID, workspaceID, "RevNext · Hotel Revenue Management", 40),
			Steps: []models.SequenceStep{
				{
					DelayDays:       0,
					SubjectTemplate: "{{name}} — {is OTA commission|are parity gaps} taxing your RevPAR?",
					BodySpintax: `Hi {{name}},

{Quick question for your property|Curious about {{name}}}: {pricing + OTAs + direct bookings|ADR, parity, and directs} — managed weekly, or {ad-hoc tweaks|whenever someone remembers}?

RevNext runs a {weekly revenue system|pricing + OTA + conversion cadence} for hotels.
{100+ properties|Since 2018 across 100+ stays} · ~35% avg revenue growth · owner-friendly RevPAR/ADR/Occ reporting.

{Free revenue audit|45-min action plan} — no obligation.
https://revnext.in

{Worth 15 minutes?|Open to a short audit call?}
RevNext`,
					VariantBSubject: "{{name}} — +35% avg revenue growth playbook (hotels)",
					VariantBBody: `Hi {{name}},

Weekly pricing + OTA fix + direct booking lift — one rhythm.
Proof: 100+ properties, clear reporting.
Free audit → https://revnext.in

RevNext`,
				},
				{
					DelayDays:       2,
					SubjectTemplate: "Re: {RevPAR audit|revenue system} for {{name}}",
					BodySpintax: `Hi {{name}},

{What owners usually see first|Typical first wins}: {parity leaks|broken promotions}, weak listing conversion, and {discounting that doesn't convert|needless rate cuts}.

{Happy to map this for {{name}}|Can walk your OTA stack} on a short call.
https://revnext.in

RevNext`,
				},
				{
					DelayDays:       5,
					SubjectTemplate: "{{name}} — {direct bookings +40%|commission saved} angle",
					BodySpintax: `Hi {{name}},

{Alongside pricing|Besides ADR work}, we push {mobile-first direct booking|your own booking path} so you're not {only renting demand from OTAs|100% OTA-dependent}.

{Audit is still free this week|Free audit still open}: https://revnext.in
Booking engine: https://booking.revnext.in

RevNext`,
				},
				{
					DelayDays:       8,
					SubjectTemplate: "{Closing the loop|Last note} — {{name}} revenue",
					BodySpintax: `Hi {{name}},

{I'll pause here|Last note}. When you want the audit: https://revnext.in · WhatsApp also on site.

RevNext`,
				},
			},
		},
		{
			Campaign: campBase(ownerID, workspaceID, "RevNext · Cloud PMS (Front Desk)", 35),
			Steps: []models.SequenceStep{
				{
					DelayDays:       0,
					SubjectTemplate: "{{name}} — front desk that {never loses the plot|stays in sync}",
					BodySpintax: `Hi {{name}},

{If HK, folios, and arrivals|When housekeeping and check-ins} live in different places, shifts get messy.

RevNext Cloud PMS: arrivals board, housekeeping, GST-ready folios, linked rooms, multi-property login.
{14-day trial · no card|Start free — cancel anytime}.
https://pms.revnext.in

{Want a 10-min walkthrough?|Open to a product tour for your desk?}
RevNext`,
					VariantBSubject: "One cloud desk for every property — {{name}}",
					VariantBBody: `Hi {{name}},

Arrivals · HK · folios · linked rooms — one Cloud PMS.
Trial: https://pms.revnext.in

RevNext`,
				},
				{
					DelayDays:       3,
					SubjectTemplate: "Re: PMS trial — {{name}}",
					BodySpintax: `Hi {{name}},

{Tip|Practical}: start with arrivals + HK board this week; folios + POS bill-to-room next.
https://pms.revnext.in · pairs with https://pos.revnext.in

{Still interested?|Want login help?}
RevNext`,
				},
				{
					DelayDays:       6,
					SubjectTemplate: "{Closing the loop|Last note} — Cloud PMS",
					BodySpintax: `Hi {{name}},

{Trial stays open|14-day trial remains}: https://pms.revnext.in

RevNext`,
				},
			},
		},
		{
			Campaign: campBase(ownerID, workspaceID, "RevNext · Cloud POS (F&B Outlets)", 35),
			Steps: []models.SequenceStep{
				{
					DelayDays:       0,
					SubjectTemplate: "{{name}} — {QR + waiters + room bills|outlet POS} in sync",
					BodySpintax: `Hi {{name}},

{Busy services|Peak hours} break when {QR orders, waiters, and Swiggy/Zomato|floor + aggregators} aren't one system.

RevNext Cloud POS: dine-in, takeaway, delivery, QR, inventory, bill-to-room via PMS.
Trial: https://pos.revnext.in

{Demo on your outlet flow?|Open to a 10-min POS tour?}
RevNext`,
					VariantBSubject: "Stop reconciling F&B bills after service — {{name}}",
					VariantBBody: `Hi {{name}},

Cloud POS built for hotel F&B + restaurants — GST, QR, aggregators, room charge.
https://pos.revnext.in

RevNext`,
				},
				{
					DelayDays:       3,
					SubjectTemplate: "Re: POS for {{name}}",
					BodySpintax: `Hi {{name}},

{If you run rooms + F&B|For hotel outlets}, POS → guest folio is the unlock (no double entry at checkout).
https://pos.revnext.in + https://pms.revnext.in

RevNext`,
				},
				{
					DelayDays:       6,
					SubjectTemplate: "{Closing the loop|Last note} — Cloud POS",
					BodySpintax: `Hi {{name}},

{Trial link stays live|Start anytime}: https://pos.revnext.in

RevNext`,
				},
			},
		},
		{
			Campaign: campBase(ownerID, workspaceID, "RevNext · Booking Engine (Direct)", 35),
			Steps: []models.SequenceStep{
				{
					DelayDays:       0,
					SubjectTemplate: "{{name}} — {keep the commission|win directs} with your own booking engine",
					BodySpintax: `Hi {{name}},

{OTA demand is fine|OTAs are great for demand} — {owning conversion is better|owning the booking path is better}.

RevNext Booking Engine: mobile-first direct booking, live ARI, payments, Google Hotel Ads feeds.
https://booking.revnext.in

{14-day trial on your site?|Want a trial widget this week?}
RevNext`,
					VariantBSubject: "Direct bookings that don't look like 2014 — {{name}}",
					VariantBBody: `Hi {{name}},

One-page booking flow · live inventory · Razorpay-ready.
https://booking.revnext.in

RevNext`,
				},
				{
					DelayDays:       2,
					SubjectTemplate: "Re: direct booking — {{name}}",
					BodySpintax: `Hi {{name}},

{Pair this with|Stack with} CMS sites (https://cms.revnext.in) + revenue rhythm (https://revnext.in) for {pricing + conversion|rate + convert} in one system.

{Still want the trial?|Shall I send setup steps?}
RevNext`,
				},
				{
					DelayDays:       5,
					SubjectTemplate: "{Closing the loop|Last note} — booking engine",
					BodySpintax: `Hi {{name}},

https://booking.revnext.in — {reply if you want onboarding help|ping me for onboarding}.

RevNext`,
				},
			},
		},
		{
			Campaign: campBase(ownerID, workspaceID, "RevNext · B2B Rates (Agents/Corporate)", 25),
			Steps: []models.SequenceStep{
				{
					DelayDays:       0,
					SubjectTemplate: "{{name}} — contract rates without {WhatsApp chaos|spreadsheet leakage}",
					BodySpintax: `Hi {{name}},

{Agent rates on PDFs|B2B rates on WhatsApp} = {disputes and leakage|parity headaches}.

RevNext Stay B2B: secure portals, contract rates, allotments, commission tracking.
https://networks.revnext.in

{Useful for your agency mix?|Want a B2B walkthrough?}
RevNext`,
					VariantBSubject: "Give agents a portal — keep control of rates",
					VariantBBody: `Hi {{name}},

B2B network for hotels × travel agents / corporates.
https://networks.revnext.in

RevNext`,
				},
				{
					DelayDays:       3,
					SubjectTemplate: "Re: B2B network — {{name}}",
					BodySpintax: `Hi {{name}},

{Roles + allotments|Per-agent access} stop "special rates" spreading outside the contract.
Trial: https://networks.revnext.in

RevNext`,
				},
				{
					DelayDays:       6,
					SubjectTemplate: "{Closing the loop|Last note} — B2B",
					BodySpintax: `Hi {{name}},

https://networks.revnext.in — {here when you need it|available whenever}.

RevNext`,
				},
			},
		},
		{
			Campaign: campBase(ownerID, workspaceID, "RevNext · Hotels Network + CMS", 30),
			Steps: []models.SequenceStep{
				{
					DelayDays:       0,
					SubjectTemplate: "{{name}} — list + site + booking in one RevNext stack",
					BodySpintax: `Hi {{name}},

{Distribution + content + booking|Listing, website, and booking} shouldn't be three disconnected vendors.

RevNext Hotels (discover/claim): https://hotels.revnext.in
Hotel CMS (themes + StreamField): https://cms.revnext.in
Booking engine: https://booking.revnext.in

{Want a stack walkthrough for {{name}}?|Open to a 15-min product tour?}
RevNext`,
					VariantBSubject: "Launch a hotel site without a custom build — {{name}}",
					VariantBBody: `Hi {{name}},

6 hotel themes, booking CTA blocks, multi-tenant CMS.
https://cms.revnext.in

RevNext`,
				},
				{
					DelayDays:       3,
					SubjectTemplate: "Re: hotel site stack — {{name}}",
					BodySpintax: `Hi {{name}},

{Editors use Wagtail|Property teams edit pages}; owners handle domains + payments — {built for operators|ops-first}.
https://cms.revnext.in

RevNext`,
				},
				{
					DelayDays:       6,
					SubjectTemplate: "{Closing the loop|Last note} — Hotels/CMS",
					BodySpintax: `Hi {{name}},

https://hotels.revnext.in · https://cms.revnext.in

RevNext`,
				},
			},
		},
	}
}

// SeedSummary is a human-readable result.
func SeedSummary(purged, templates, campaigns int) string {
	return fmt.Sprintf("Purged %d dummy leads; seeded %d templates, %d campaigns (skipped existing).", purged, templates, campaigns)
}
