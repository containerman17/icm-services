// Copyright (C) 2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package config

import "github.com/spf13/pflag"

func BuildFlagSet() *pflag.FlagSet {
	fs := pflag.NewFlagSet("signature-aggregator", pflag.ContinueOnError)
	fs.String(ConfigFileKey, "", "Specifies the relayer config file")
	fs.BoolP(VersionKey, "", false, "Display awm-relayer version")
	fs.BoolP(HelpKey, "", false, "Display awm-relayer usage")
	return fs
}
