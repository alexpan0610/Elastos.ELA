package main

import (
	"os"

	cmdcom "github.com/elastos/Elastos.ELA/cmd/common"
	"github.com/elastos/Elastos.ELA/cmd/info"
	"github.com/elastos/Elastos.ELA/cmd/mine"
	"github.com/elastos/Elastos.ELA/cmd/rollback"
	"github.com/elastos/Elastos.ELA/cmd/script"
	"github.com/elastos/Elastos.ELA/cmd/wallet"

	"github.com/urfave/cli"
)

var Version string

func main() {
	app := cli.NewApp()
	app.Name = "ela-cli"
	app.Version = Version
	app.HelpName = "ela-cli"
	app.Usage = "command line tool for ELA blockchain"
	app.UsageText = "ela-cli [global options] command [command options] [args]"
	app.HideHelp = false
	app.HideVersion = false
	//commands
	app.Commands = []cli.Command{
		*wallet.NewCommand(),
		*info.NewCommand(),
		*mine.NewCommand(),
		*script.NewCommand(),
		*rollback.NewCommand(),
	}

	//sort.Sort(cli.CommandsByName(app.Commands))
	//sort.Sort(cli.FlagsByName(app.Flags))

	if err := app.Run(os.Args); err != nil {
		cmdcom.PrintErrorMsg(err.Error())
		os.Exit(1)
	}
}
