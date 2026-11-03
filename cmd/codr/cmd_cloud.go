package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codr.my/codr/internal/cloud"
	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/store"
)

// `codr cloud ls|run|restore` (D70). ls and run use the configuration stored by /admin/sandaran; restore is
// the disaster path and needs NOTHING from the old server: bucket details as flags, the bucket secret in
// CODR_CLOUD_SECRET and the recovery key in CODR_RECOVERY_KEY (never on the command line, never in history).
func (c *cli) cmdCloud(args []string) error {
	if len(args) == 0 {
		return c.usage("cli.need_arg", "ls | run | restore")
	}
	switch args[0] {
	case "ls", "run":
		return c.cloudLocal(args[0])
	case "restore":
		return c.cloudRestore(args[1:])
	}
	return c.usage("cli.bad_flag", args[0])
}

func (c *cli) cloudJob() (*cloud.Job, *store.Store, error) {
	cfg, err := c.loadConfig()
	if err != nil {
		return nil, nil, err
	}
	key, err := secret.Parse(cfg.Auth.SecretKey)
	if err != nil || key == nil {
		return nil, nil, c.usage("cloud.no_secret_key")
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return nil, nil, err
	}
	return &cloud.Job{Store: st, Disk: repos.Disk{Root: cfg.ReposDir(), GitBinary: cfg.Git.Binary}, GitBinary: cfg.Git.Binary,
		ConfigPath: c.configPath, WorkDir: filepath.Join(cfg.DataDir(), ".cloud-tmp"), Secret: key}, st, nil
}

func (c *cli) cloudLocal(verb string) error {
	job, st, err := c.cloudJob()
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	if verb == "run" {
		out, err := job.Run(ctx, "cli")
		if err != nil {
			return err
		}
		fmt.Fprintln(c.out, c.t("cloud.run_done", out.Object, out.Bytes, out.Repos, out.Pruned))
		return nil
	}
	cfg, err := st.CloudConfig(ctx)
	if err != nil {
		return err
	}
	cl, err := job.Client(cfg)
	if err != nil {
		return c.usage("cloud.not_configured")
	}
	objs, err := cl.List(ctx, cloud.ObjectPrefix(cfg))
	if err != nil {
		return err
	}
	for _, o := range objs {
		fmt.Fprintln(c.out, c.t("cloud.ls_row", o.Key, o.Size, o.Modified.Format(time.RFC3339)))
	}
	return nil
}

func (c *cli) cloudRestore(args []string) error {
	fs := flag.NewFlagSet("cloud restore", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	ep := fs.String("endpoint", "", "")
	region := fs.String("region", "auto", "")
	bucket := fs.String("bucket", "", "")
	prefix := fs.String("prefix", "codr", "")
	access := fs.String("access-key", "", "")
	object := fs.String("object", "", "")
	dir := fs.String("dir", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *ep == "" || *bucket == "" || *access == "" || *dir == "" {
		return c.usage("cli.need_arg", "--endpoint URL --bucket NAME --access-key ID --dir EMPTY_DIR")
	}
	sk, rk := os.Getenv("CODR_CLOUD_SECRET"), os.Getenv("CODR_RECOVERY_KEY")
	if sk == "" || rk == "" {
		return c.usage("cloud.need_env")
	}
	key, err := cloud.ParseRecoveryKey(rk)
	if err != nil {
		return c.usage("cloud.bad_key")
	}
	if entries, err := os.ReadDir(*dir); err == nil && len(entries) > 0 {
		return c.usage("restore.not_empty", *dir)
	}
	cl := &cloud.Client{Endpoint: *ep, Region: *region, Bucket: *bucket, AccessKey: *access, SecretKey: sk}
	ctx := context.Background()
	name := *object
	if name == "" {
		objs, err := cl.List(ctx, cloud.ObjectPrefix(store.CloudConfig{Prefix: *prefix}))
		if err != nil {
			return err
		}
		for _, o := range objs {
			if strings.HasSuffix(o.Key, ".tar.gz.enc") {
				name = o.Key // keys sort by their UTC stamp: the last is the newest
			}
		}
		if name == "" {
			return c.usage("cloud.none_found", *bucket, *prefix)
		}
	}
	tmp, err := os.MkdirTemp("", "codr-pulih-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	fmt.Fprintln(c.out, c.t("cloud.fetching", name))
	snap, err := cloud.Fetch(ctx, cl, name, key, tmp)
	if err != nil {
		return err
	}
	return c.cmdRestore([]string{snap, "--dir", *dir})
}

// cloudDoctor is the doctor line for cloud backups: silent when they are off.
func (d *doctor) cloud(ctx context.Context, st *store.Store) {
	cfg, err := st.CloudConfig(ctx)
	if err != nil || !cfg.Enabled {
		return
	}
	runs, err := st.CloudRuns(ctx, 1)
	switch {
	case err != nil || len(runs) == 0:
		d.line("warn", "doctor.cloud_none")
	case runs[0].Status == "failed":
		d.line("fail", "doctor.cloud_failed", runs[0].StartedAt, runs[0].Detail)
	default:
		t, err := time.Parse(time.RFC3339Nano, runs[0].StartedAt)
		if err != nil || time.Since(t) > 2*time.Duration(cfg.IntervalHours)*time.Hour {
			d.line("warn", "doctor.cloud_stale", runs[0].StartedAt)
		} else {
			d.line("ok", "doctor.cloud", runs[0].Object, runs[0].StartedAt)
		}
	}
}
