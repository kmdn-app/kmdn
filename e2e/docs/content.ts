// The sample repository the documentation screenshots use: Northwind's
// handbook, with a few contradictions for the consistency check to find.

export const REPO = "northwind/handbook";

export const PEOPLE = {
  maya: { email: "maya@northwind.test", name: "Maya Chen" }, // instance admin, docs lead
  sam: { email: "sam@northwind.test", name: "Sam Lindqvist" }, // maintainer
  tom: { email: "tom@northwind.test", name: "Tom Okafor" }, // contributor (HR)
  priya: { email: "priya@northwind.test", name: "Priya Raman" }, // contributor (engineering)
  luis: { email: "luis@northwind.test", name: "Luis Ortega" }, // viewer (new hire)
};

export const INITIAL: Record<string, string> = {
  "README.md": "# Northwind handbook\n\nThe source of the Northwind employee handbook. Edit it in kmdn.\n",
  ".kmdn.yml": 'root: docs/\ninclude: ["**/*.md", "**/*.{png,jpg,svg,webp}"]\nassets:\n  path: "{dir}/images/{name}.{ext}"\n',
  "docs/index.md": `---
title: Northwind handbook
---

# Northwind handbook

Everything you need to work at Northwind. If something here is wrong, fix it: every page can be edited by anyone on the team, and the docs team reviews each change.

## Start here

- [Your first week](onboarding/first-week.md)
- [IT setup](onboarding/it-setup.md)
- [Remote work](policies/remote-work.md)
- [Travel](policies/travel.md)

## Engineering

- [Deploy runbook](engineering/deploy-runbook.md)
- [Incident response](engineering/incident-response.md)
`,
  "docs/onboarding/first-week.md": `---
title: Your first week
owner: people-team
---

# Your first week

Welcome to Northwind. This page walks you through the first five days.

## Monday

- Pick up your laptop at the IT desk (see [IT setup](it-setup.md)).
- Meet your onboarding buddy for lunch.
- Read the [remote work policy](../policies/remote-work.md).

## Tuesday to Thursday

| Day | Morning | Afternoon |
|-----|---------|-----------|
| Tuesday | Product tour | Shadow a support shift |
| Wednesday | Team rituals | First small task |
| Thursday | Security training | Pair with your buddy |

## Friday

Share one thing that surprised you in the #welcome channel.
`,
  "docs/onboarding/it-setup.md": `---
title: IT setup
---

# IT setup

## Your laptop

Every new hire gets a MacBook Pro. Laptops are refreshed every four years.

1. Sign in with your Northwind account.
2. Turn on FileVault when asked.
3. Install the apps from Self Service.

## Accounts

You get access to email, chat and the handbook on day one. Ask your manager for anything else.

\`\`\`bash
# Check that your VPN profile is installed
scutil --nc list | grep Northwind
\`\`\`
`,
  "docs/policies/remote-work.md": `---
title: Remote work
owner: people-team
---

# Remote work

Northwind is remote-friendly. Most teams meet in the office on Tuesdays.

## Working abroad

You can work from another country for up to 30 working days a year. Tell your manager two weeks ahead.

## Equipment

Ask the IT desk for a monitor and a chair for your home office.
`,
  "docs/policies/travel.md": `---
title: Travel
---

# Travel

## Booking

Book trains and flights through the travel desk. Trains are preferred for trips under five hours.

## Working abroad

Employees may work abroad for up to 20 working days per calendar year.

## Expenses

Submit receipts within 30 days.
`,
  "docs/policies/laptop-policy.md": `---
title: Laptop policy
---

# Laptop policy

Laptops are refreshed every three years. Report a lost laptop to the IT desk within 24 hours.
`,
  "docs/engineering/deploy-runbook.md": `---
title: Deploy runbook
owner: platform
---

# Deploy runbook

Production deploys happen from the \`main\` branch.

\`\`\`mermaid
flowchart LR
  PR[Pull request] --> CI[CI checks] --> Staging --> Production
\`\`\`

## Before you deploy

- CI is green on \`main\`.
- Nobody else is deploying (check #deploys).

## Rolling back

Redeploy the previous release from the deploy dashboard. See [incident response](incident-response.md).
`,
  "docs/engineering/incident-response.md": `---
title: Incident response
owner: platform
---

# Incident response

1. Open an incident in #incidents.
2. Name an incident lead.
3. Post updates every 30 minutes.

See the [deploy runbook](deploy-runbook.md) for rollbacks.
`,
};

// Commits pushed straight to main after the first one, so history and
// "updated since your last visit" have something to show.
export const LATER: {
  author: { name: string; email: string };
  message: string;
  files: Record<string, string>;
}[] = [
  {
    author: PEOPLE.priya,
    message: "Deploy runbook: add the staging step",
    files: {
      "docs/engineering/deploy-runbook.md": INITIAL["docs/engineering/deploy-runbook.md"]!.replace(
        "- Nobody else is deploying (check #deploys).",
        "- Nobody else is deploying (check #deploys).\n- The release is on staging for at least an hour.",
      ),
    },
  },
];
