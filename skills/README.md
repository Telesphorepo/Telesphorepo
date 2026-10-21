# Agent skills

With the skill [`telesphorepo-migrate`](telesphorepo-migrate/SKILL.md), a coding agent moves a
project from Nexus to Telesphorepo with you. The agent changes the download addresses in every file of the
project and, for the full switch-over, sets up `telesphorepo publish`, its CI steps and a
rehearsal on a test repository. You do only the steps that need a person: approving the computer,
making a read token, making repositories and the CI pipeline. The agent asks for each one and
waits.

## Install it for Claude Code

Copy the folder `telesphorepo-migrate` into one of these folders:

- `.claude/skills/` of a project, for that project only
- `~/.claude/skills/` of your user, for every project

```sh
git clone --depth 1 https://github.com/Telesphorepo/Telesphorepo.git /tmp/telesphorepo
mkdir -p ~/.claude/skills
cp -r /tmp/telesphorepo/skills/telesphorepo-migrate ~/.claude/skills/
```

On Windows, the user folder is `%USERPROFILE%\.claude\skills\`.

Then ask Claude Code in the project folder, for example: "Move this project from Nexus to
Telesphorepo."

## Agents without skills

For an agent that does not load skills, paste a prompt from
[Move your projects](https://docs.telesphorepo.com/move-projects/). Choose the ecosystem and the
scope there; each prompt follows the same steps as the skill.
