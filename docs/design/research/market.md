# Market research: Nexus, Artifactory and alternatives

Collected 2026-10-14. Primary sources unless marked
SECONDARY. Items under "Unverified" must not be used as facts.

## Strongest verified points

1. **Nexus Pro starts at $7,500/year (self-hosted)**, priced by consumption: 50K components and
   15M requests/month included. Cloud Pro starts at $1,950/year plus storage and egress.
   https://www.sonatype.com/products/pricing
2. **JFrog Artifactory self-hosted starts at $27,000/year** (Pro X, 1 server); Enterprise X from
   $51,000/year (3 servers). https://jfrog.com/pricing/
3. **JFrog cloud Pro is $150/month list**, $50/month under a promotion "available only through
   June 4th 2027", plus per-GB charges above 25 GB. https://jfrog.com/buy-now/pro
4. **Nexus "OSS" became "Community Edition" in 3.77 (2025-02-04)** with an EULA you must accept
   to upload or fetch, and usage limits.
   https://community.sonatype.com/t/sonatype-nexus-repository-oss-is-now-community-edition/14324,
   https://help.sonatype.com/en/ce-onboarding.html
5. **Nexus 3.87.0 (2025-12-02) cut the Community Edition to 40,000 components and 100,000
   requests/day** (from 100K / 200K). Over a limit, the server goes read-only: "users are not able
   to add new components" until usage drops or you buy Pro. Sonatype staff: "to keep the product sustainable".
   https://help.sonatype.com/en/sonatype-nexus-repository-3-87-0-release-notes.html,
   https://help.sonatype.com/en/usage-center.html,
   https://community.sonatype.com/t/sonatype-nexus-repository-3-87-0-released/15852
6. **Failed requests (401s, HEADs) count toward the daily limit**, so junk traffic against a public
   instance can use it up: sonatype/nexus-public#736 (opened 2025-09-23).
   https://github.com/sonatype/nexus-public/issues/736
7. **OrientDB was dropped in 3.71 (2024-08-08)**; the forced migration to H2/PostgreSQL broke
   uploads for many: #449 "Cannot upload after OrientDB to H2 migration" open since 2024-08-12,
   90 comments; #451 open, 42 comments.
   https://help.sonatype.com/en/sonatype-nexus-repository-3-71-0-release-notes.html,
   https://github.com/sonatype/nexus-public/issues/449, https://github.com/sonatype/nexus-public/issues/451
8. **Staying behind is unsafe**: Sonatype lists seven OrientDB CVEs (two critical, per Sonatype)
   and declared OrientDB support "fully sunset" (2026-04-09).
   https://help.sonatype.com/en/orientdb-downloads.html,
   https://www.sonatype.com/blog/modernizing-nexus-repository-moving-beyond-orientdb
9. **Nexus 2 was sunset on 2025-06-30.** Nexus 3 has had RCEs, e.g. CVE-2020-10199 (CVSS 8.8, on
   CISA KEV since 2021-11-03).
   https://help.sonatype.com/en/sonatype-nexus-repository-2-sunsetting-information.html
10. **Leaving is hard**: JFrog's nexus2artifactory migrator is deprecated, migrates only to paid
    Artifactory, and drops password hashes. Artifactory OSS supports only Maven, Gradle, Ivy, SBT
    and Generic, on 1 server with 3 projects. https://github.com/jfrog/nexus2artifactory,
    https://docs.jfrog.com/installation/docs/feature-comparison-matrix-for-self-mangaged-jpds
11. **Pro-only in Nexus** (feature matrix): HA, SAML, content replication, staging, user tokens,
    Azure/Google blob stores, export assets, branding.
    https://help.sonatype.com/en/nexus-repository-feature-matrix.html,
    https://help.sonatype.com/en/branding-capability.html

12. **Maven Central has publishing limits from 2026-10-01**, per organization and per calendar
    month, set where the top 10% of publishers begin: about 7 releases, 78 MB and 1,167 files.
    Over a limit, a 30-day grace period starts; after it, new releases that keep the
    organization over the limit are refused. Artifacts for a commercial product need Publisher
    Pro at any volume. https://central.sonatype.org/publish/maven-central-publishing-limits/,
    https://community.sonatype.com/t/maven-central-publishing-limits-are-now-in-effect/16675

## Alternatives (one line each)

- Reposilite: free, Apache-2.0, JVM (Maven) only. https://github.com/dzikoysk/reposilite
- Forgejo packages: built into Forgejo, 24 package types. https://forgejo.org/docs/latest/user/packages/
- GitHub Packages: 500 MB / 1 GB transfer on Free, more on paid plans.
  https://docs.github.com/en/billing/concepts/product-billing/github-packages
- AWS CodeArtifact: pay per GB-month, request and transfer. https://aws.amazon.com/codeartifact/pricing/
- Cloudsmith: SECONDARY only (pricing page rate-limited); verify before use.

## Sentiment (quotes, ≤ 25 words)

- "Yes, that end with a DOS in a public instance" — GitHub #736 commenter, Sept 2025.
- The limit "already caused significant disruption within our IT team" — Ulric Cuvelier, Sonatype
  forum, 2025-12-09.
- "Your justification makes no sense and is unwarranted." — jonas.attia, Sonatype forum, 2025-12-02.

## Unverified — do not use as fact

- The CE grace period (45 vs 30 days) and whether it still applies after 3.87.
- That welcome/home page customisation was removed from OSS (only branding is documented Pro-only).
- The OrientDB CVE IDs (CVE-2026-*) — per Sonatype's page, not checked in NVD.
- Nexus 2 EOL "2024-06-30" (third-party). A JFrog free cloud tier. Cloudsmith, CodeArtifact and
  GitHub Packages overage rates.
