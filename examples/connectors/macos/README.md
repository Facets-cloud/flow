# macOS dialog connector

An opt-in [flow human connector](../../../internal/app/skill/references/connectors.md)
(contract 2, role `human`). When an agent asks you something with

```bash
flow message user "Deploy v2 to prod?" --options "Yes|No"
```

flow pings you through this connector: a native macOS dialog pops up. Your
answer goes back to the asking task's inbox as a reply (`reply_to=<id>`),
and the agent picks it up there. The agent never blocks waiting on you.

```
agent ── flow message user "q" --options "A|B" ──► flow (row stored, ping decided)
                                                    │ detached dispatch
                                                    ▼
                                     run send  ──► dialog (forked, never auto-dismisses)
                                                    │ you click "A" + optional note
                                                    ▼
             "$FLOW_BIN" human reply <id> "A — note" ──► user/<task> inbox (reply_to=<id>)
```

## Install

```bash
flow connector add macos                       # built into flow: copies it, stores a secret, shows a test dialog
flow connector add examples/connectors/macos   # same, from a repo checkout
flow connector list
flow connector test macos                      # another test dialog
flow connector remove macos                    # uninstall (folder + secret)
```

Requirements: stock macOS only (bash + `osascript`). No Homebrew, jq or
Python is needed. The dialog uses a plain `display dialog` (not `tell
application "System Events"`), so you don't grant any Automation
permission.

## What you see

| The ask has | Dialog |
|---|---|
| no options | your message, a text field, and the buttons **Later** and **Reply**. The reply is your text. Replying with an empty field marks the ask seen. |
| 1–2 options | your message, a text field (an optional note), **Later**, and one button per option. The reply is `<option> — <note>`, or just `<option>`. |
| 3+ options | a pick list, then a note dialog with **Later** and **Send**. |

- The dialog title is `flow · <task>`. Urgent asks get the prefix `URGENT:`.
- **Later** (or closing the dialog) does nothing, and the mail stays unread
  in `flow inbox`. If one of the options is itself "Later", the dismiss
  button reads **Not now** instead.
- Dialogs never auto-dismiss. flow sends one ping per task until you've
  seen it, so dialogs don't pile up from a chatty agent. Nothing is ever
  re-popped.

## Mail from a peer hub — "Hand to agent" (example)

When the message came from another flow hub (a peer, see
`references/peers.md`), the dialog is titled `flow · from <peer>`, lists any
choices in the text, and offers three buttons: **Later**, **Hand to agent**,
**Reply**.

- **Reply** sends what you typed back to that peer (`flow human reply`).
- **Hand to agent** marks the mail seen (`flow human seen <id>`) and starts a
  headless agent with `flow auto "Answer <peer>: …" --brief -`. The brief
  carries the question, the note you typed, and the instruction to answer
  with `flow message <peer> --reply-to <id> "…"` and then `flow done`.

This button is the connector's own example logic, not a flow feature. Copy
it and change it to suit you.

## Files

- `connector.json` is the manifest (`events: ["ask","urgent"]`).
- `run` implements `send` and `test`. It parses the JSON with JXA and then
  forks the dialog, so it returns to flow within about a second. Dialog
  errors go to `state/dialog.log` inside the installed folder.
