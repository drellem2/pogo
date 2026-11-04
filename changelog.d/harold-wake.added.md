- **`pogo hook harold-wake`, harold's notify hook.** When harold receives a
  Discord ping for an identity, it can now run `~/.pogo/bin/harold-wake`
  (written by `pogo install`). The hook mails the pogo agent behind that
  identity: `Discord: <n> new pings; call discord_poll`, from `harold`. The
  identity's own name is the agent, unless `~/.pogo/harold-wake.conf` maps it to
  another. Only the count is passed on. The ping's text never reaches the mail,
  because a stranger wrote it. An identity with no coordinator or crew agent
  behind it wakes nobody. See docs/CONFIGURATION.md, "Waking an agent on
  Discord activity (harold)".
