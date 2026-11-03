package main

import (
	"fmt"

	"codr.my/codr/internal/auth"
)

// Recovery from the server, for the person who runs it (D80): a lost phone and lost recovery codes, or a forgotten
// password. Both sign the person out everywhere, so whoever might hold an old session loses it too.

func (a *admin) user2FAOff(args []string) error {
	name, _, err := a.positional(args, "NAME")
	if err != nil {
		return err
	}
	u, err := a.st.UserByName(a.ctx, name)
	if err != nil || u.IsOrg {
		return a.usage("user.not_found", name)
	}
	if err := a.st.DisableTOTP(a.ctx, u.ID); err != nil {
		return err
	}
	n, _ := a.st.EndOtherSessions(a.ctx, u.ID, "")
	_ = a.st.Audit(a.ctx, 0, "user.2fa_off", u.Name, "cli")
	fmt.Fprintln(a.out, a.t("user.2fa_off", u.Name, n))
	return nil
}

func (a *admin) userPasswd(args []string) error {
	name, _, err := a.positional(args, "NAME")
	if err != nil {
		return err
	}
	u, err := a.st.UserByName(a.ctx, name)
	if err != nil || u.IsOrg {
		return a.usage("user.not_found", name)
	}
	pw, err := a.readPassword()
	if err != nil {
		return err
	}
	if len(pw) < a.cfg.Auth.PasswordMinLen {
		return a.usage("user.password_short", a.cfg.Auth.PasswordMinLen)
	}
	hash, err := auth.Hash(pw, auth.Params{Time: a.cfg.Auth.Argon2Time, Memory: a.cfg.Auth.Argon2MemoryKiB, Threads: a.cfg.Auth.Argon2Threads})
	if err != nil {
		return err
	}
	if err := a.st.SetPasswordHash(a.ctx, u.ID, hash); err != nil {
		return err
	}
	n, _ := a.st.EndOtherSessions(a.ctx, u.ID, "")
	_ = a.st.Audit(a.ctx, 0, "user.password", u.Name, "cli")
	fmt.Fprintln(a.out, a.t("user.password_set", u.Name, n))
	return nil
}
