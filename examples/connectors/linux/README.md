# Linux desktop connector

An opt-in [flow human connector](../../../internal/app/skill/references/connectors.md)
(contract v1), the Linux counterpart of the [macOS dialog](../macos). When an
agent asks you something with

```bash
flow message user "Deploy v2 to prod?" --options "Yes|No"
```

flow pings you through this connector: a desktop dialog pops up. Your
answer goes back to the asking task's inbox as a reply (`reply_to=<id>`),
and the agent picks it up there. The agent never blocks waiting on you.

```
agent ── flow message user "q" --options "A|B" ──► flow (row stored, ping decided)
                                                    │ detached dispatch
                                                    ▼
                                     run send  ──► zenity | kdialog dialog (forked, never auto-dismisses)
                                                    │ you pick "A" + optional note
                                                    ▼
             "$FLOW_BIN" human reply <id> "A — note" ──► user/<task> inbox (reply_to=<id>)
```

## Install

```bash
flow connector add linux                       # built into flow: copies it, stores a secret, sends a test notification
flow connector add examples/connectors/linux   # same, from a repo checkout
flow connector list
flow connector test linux                      # another test notification
flow connector remove linux                    # uninstall (folder + secret)
```

Requirements: bash, `jq` or `python3`, and a desktop session (`DISPLAY` or
`WAYLAND_DISPLAY` set) with at least one of:

| Tool | Package (Debian/Ubuntu · Fedora · Arch) | Used for |
|---|---|---|
| `zenity` | `zenity` · `zenity` · `zenity` | the ask dialog (preferred) |
| `kdialog` | `kdialog` · `kdialog` · `kdialog` | the ask dialog when zenity is absent |
| `notify-send` | `libnotify-bin` · `libnotify` · `libnotify` | the test toast; the ask itself when no dialog tool exists |

The secret lives in a 0600 file under `$FLOW_ROOT/secrets/` (there is no
keychain on Linux).

## What you see

| The ask has | Dialog |
|---|---|
| no options | your message and a text field. The reply is your text; an empty reply marks the ask seen. |
| options | a pick list of the options, then a note box ("Add a note (optional)"). The reply is `<option> — <note>`, or just `<option>`. |

- The dialog title is `flow - <task>`. Urgent asks get the prefix `URGENT:`.
- **Cancel** (or closing a dialog) does nothing, and the mail stays unread in
  `flow inbox`.
- Dialogs never auto-dismiss. flow sends one ping per task until you've seen
  it, so dialogs don't pile up from a chatty agent.
- With `notify-send` but no `zenity`/`kdialog`, the ask arrives as a
  notification listing the options and how to answer from a terminal
  (`flow inbox --as user`).

## Headless boxes

On a machine with no display (SSH, a container, a server) `run send` exits 1
with `no display … the ask stays in flow inbox`. flow logs that in
`$FLOW_ROOT/connectors/dispatch.log`, tries the next connector tier if you
have one (a chat hub, for example), and the ask waits in `flow inbox` either
way. `flow connector add linux` on such a box fails its test for the same
reason, on purpose: install it where you have a desktop.

## Files

- `connector.json` is the manifest (`events: ["ask","urgent"]`).
- `run` implements `send` and `test`. It parses the JSON with `jq` (or
  `python3`), then detaches the dialog with `setsid`, so it returns to flow
  within about a second. Dialog errors go to `state/dialog.log` inside the
  installed folder.
