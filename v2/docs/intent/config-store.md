# Intent: Persistent config store for the `aa` tool

## Problem
- User must supply the Fly.io API token on every command invocation (flag or environment variable).
- Re-pasting secrets each time is tedious, error-prone, and exposes the value in shell history.
- No single place records the user's preferences for the tool; future settings would face the same problem.
- User has no way to see which preferences are currently in effect, or to clear one without editing hidden state.

## Personas
- **Solo operator (primary).** Runs `aa` many times a day from their own laptop. Currently pastes or re-exports the token every session; loses time and leaks the token into shell history. Wants to set it once and forget it.
- **Returning user after a break.** Comes back to the tool weeks later, forgets what's been configured. Wants to ask the tool what it knows about them without grepping around.
- **User rotating or revoking a credential.** Wants to remove the old value cleanly and set a new one without leaving stale data behind.

## Success criteria
- After the user stores the Fly.io token once, subsequent commands that need it succeed without the user supplying it again in any form.
- The user can run a single command to list every value they have stored, with keys clearly labeled.
- The user can run a single command to remove an individual stored value; afterwards it no longer appears in the listing and commands that needed it prompt the user to set it.
- Attempting to read the stored values as a different user account on the same machine is refused by the operating system.
- When a command needs a value that has not been stored, the error message names the exact key the user must set and how to set it.
- Storing a second, unrelated key (today hypothetical, tomorrow real) works through the same command surface with no new concepts to learn.
- Setting a key that is already stored replaces the old value and the listing reflects the new one.

## Non-goals
- No syncing of config across machines, users, or teams.
- No encryption-at-rest with a user passphrase or OS keychain integration.
- No per-project or per-directory config layering — one config per user on the machine.
- No import/export, backup, or migration tooling.
- No auditing, history, or "who changed what" tracking of config changes.
- No interactive prompt-and-confirm UX for setting values; the command surface is non-interactive.
- No validation that a stored credential is actually accepted by the upstream service at set time.

## Constraints
- Solo developer, LLM-authored code, no third-party runtime dependencies.
- Must work on the operating systems the `aa` CLI already runs on, with no extra install step.
- Stored secrets must not be readable by other local user accounts under default OS permissions.
- Command output must remain greppable and stable enough for the user to script against.
- Error messages must tell the user what went wrong and what to do next.

## Open questions
- When listing stored values, should secret-like values be shown in full, masked, or masked-by-default with an opt-in to reveal?
- Should removing the last remaining value also remove any on-disk artifact, or leave an empty container behind?
- Is there a reserved namespace or naming convention for keys (e.g. `token.<service>`, `pref.<name>`), and is that user-visible or internal?
- When a command can get a value from more than one source (stored config, environment, flag), what is the documented precedence and is it surfaced to the user anywhere?
- Should setting a value echo the value back, echo only the key, or stay silent on success?
