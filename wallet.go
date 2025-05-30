package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip60"
	"github.com/nbd-wtf/go-nostr/nip61"
	"github.com/nbd-wtf/go-nostr/sdk"
	"github.com/urfave/cli/v3"
)

func prepareWallet(ctx context.Context, c *cli.Command) (*nip60.Wallet, func(), error) {
	kr, _, err := gatherKeyerFromArguments(ctx, c)
	if err != nil {
		return nil, nil, err
	}

	pk, err := kr.GetPublicKey(ctx)
	if err != nil {
		return nil, nil, err
	}

	relays := sys.FetchOutboxRelays(ctx, pk, 3)
	w := nip60.LoadWallet(ctx, kr, sys.Pool, relays)
	if w == nil {
		return nil, nil, fmt.Errorf("error loading walle")
	}

	w.Processed = func(evt *nostr.Event, err error) {
		if err == nil {
			logverbose("processed event %s\n", evt)
		} else {
			log("error processing event %s: %s\n", evt, err)
		}
	}

	w.PublishUpdate = func(event nostr.Event, deleted, received, change *nip60.Token, isHistory bool) {
		desc := "wallet"
		if received != nil {
			mint, _ := strings.CutPrefix(received.Mint, "https://")
			desc = fmt.Sprintf("received from %s with %d proofs totalling %d",
				mint, len(received.Proofs), received.Proofs.Amount())
		} else if change != nil {
			mint, _ := strings.CutPrefix(change.Mint, "https://")
			desc = fmt.Sprintf("change from %s with %d proofs totalling %d",
				mint, len(change.Proofs), change.Proofs.Amount())
		} else if deleted != nil {
			mint, _ := strings.CutPrefix(deleted.Mint, "https://")
			desc = fmt.Sprintf("deleting a used token from %s with %d proofs totalling %d",
				mint, len(deleted.Proofs), deleted.Proofs.Amount())
		} else if isHistory {
			desc = "history entry"
		}

		log("- saving kind:%d event (%s)... ", event.Kind, desc)
		first := true
		for res := range sys.Pool.PublishMany(ctx, relays, event) {
			cleanUrl, _ := strings.CutPrefix(res.RelayURL, "wss://")
			if !first {
				log(", ")
			}
			first = false

			if res.Error != nil {
				log("%s: %s", colors.errorf(cleanUrl), res.Error)
			} else {
				log("%s: ok", colors.successf(cleanUrl))
			}
		}
		log("\n")
	}

	<-w.Stable

	return w, func() {
		w.Close()
	}, nil
}

var wallet = &cli.Command{
	Name:                      "wallet",
	Usage:                     "displays the current wallet balance",
	Description:               "all wallet data is stored on Nostr relays, signed and encrypted with the given key, and reloaded again from relays on every call.\n\nthe same data can be accessed by other compatible nip60 clients.",
	DisableSliceFlagSeparator: true,
	Flags:                     defaultKeyFlags,
	Action: func(ctx context.Context, c *cli.Command) error {
		w, closew, err := prepareWallet(ctx, c)
		if err != nil {
			return err
		}

		stdout(w.Balance())

		closew()
		return nil
	},
	Commands: []*cli.Command{
		{
			Name:                      "mints",
			Usage:                     "lists, adds or remove default mints from the wallet",
			DisableSliceFlagSeparator: true,
			Action: func(ctx context.Context, c *cli.Command) error {
				w, closew, err := prepareWallet(ctx, c)
				if err != nil {
					return err
				}

				for _, url := range w.Mints {
					stdout(strings.Split(url, "://")[1])
				}

				closew()
				return nil
			},
			Commands: []*cli.Command{
				{
					Name:                      "add",
					DisableSliceFlagSeparator: true,
					ArgsUsage:                 "<mint>...",
					Action: func(ctx context.Context, c *cli.Command) error {
						w, closew, err := prepareWallet(ctx, c)
						if err != nil {
							return err
						}

						if err := w.AddMint(ctx, c.Args().Slice()...); err != nil {
							return err
						}

						closew()
						return nil
					},
				},
				{
					Name:                      "remove",
					DisableSliceFlagSeparator: true,
					ArgsUsage:                 "<mint>...",
					Action: func(ctx context.Context, c *cli.Command) error {
						w, closew, err := prepareWallet(ctx, c)
						if err != nil {
							return err
						}

						if err := w.RemoveMint(ctx, c.Args().Slice()...); err != nil {
							return err
						}

						closew()
						return nil
					},
				},
			},
		},
		{
			Name:                      "tokens",
			Usage:                     "lists existing tokens with their mints and aggregated amounts",
			DisableSliceFlagSeparator: true,
			Action: func(ctx context.Context, c *cli.Command) error {
				w, closew, err := prepareWallet(ctx, c)
				if err != nil {
					return err
				}

				for _, token := range w.Tokens {
					stdout(token.ID(), token.Proofs.Amount(), strings.Split(token.Mint, "://")[1])
				}

				closew()
				return nil
			},
		},
		{
			Name:                      "receive",
			Usage:                     "takes a cashu token string as an argument and adds it to the wallet",
			ArgsUsage:                 "<token>",
			DisableSliceFlagSeparator: true,
			Flags: []cli.Flag{
				&cli.StringSliceFlag{
					Name:  "mint",
					Usage: "mint to swap the token into",
				},
			},
			Action: func(ctx context.Context, c *cli.Command) error {
				args := c.Args().Slice()
				if len(args) != 1 {
					return fmt.Errorf("must be called as `nak wallet receive <token>")
				}

				w, closew, err := prepareWallet(ctx, c)
				if err != nil {
					return err
				}

				proofs, mint, err := nip60.GetProofsAndMint(args[0])
				if err != nil {
					return err
				}

				opts := make([]nip60.ReceiveOption, 0, 1)
				for _, url := range c.StringSlice("mint") {
					opts = append(opts, nip60.WithMintDestination(url))
				}

				if err := w.Receive(ctx, proofs, mint, opts...); err != nil {
					return err
				}

				closew()
				return nil
			},
		},
		{
			Name:                      "send",
			Usage:                     "prints a cashu token with the given amount for sending to someone else",
			ArgsUsage:                 "<amount>",
			DisableSliceFlagSeparator: true,
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:  "mint",
					Usage: "send from a specific mint",
				},
			},
			Action: func(ctx context.Context, c *cli.Command) error {
				args := c.Args().Slice()
				if len(args) != 1 {
					return fmt.Errorf("must be called as `nak wallet send <amount>")
				}
				amount, err := strconv.ParseUint(args[0], 10, 64)
				if err != nil {
					return fmt.Errorf("amount '%s' is invalid", args[0])
				}

				w, closew, err := prepareWallet(ctx, c)
				if err != nil {
					return err
				}

				opts := make([]nip60.SendOption, 0, 1)
				if mint := c.String("mint"); mint != "" {
					mint = "http" + nostr.NormalizeURL(mint)[2:]
					opts = append(opts, nip60.WithMint(mint))
				}
				proofs, mint, err := w.Send(ctx, amount, opts...)
				if err != nil {
					return err
				}

				stdout(nip60.MakeTokenString(proofs, mint))

				closew()
				return nil
			},
		},
		{
			Name:                      "pay",
			Usage:                     "pays a bolt11 lightning invoice and outputs the preimage",
			ArgsUsage:                 "<invoice>",
			DisableSliceFlagSeparator: true,
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:  "mint",
					Usage: "pay from a specific mint",
				},
			},
			Action: func(ctx context.Context, c *cli.Command) error {
				args := c.Args().Slice()
				if len(args) != 1 {
					return fmt.Errorf("must be called as `nak wallet pay <invoice>")
				}

				w, closew, err := prepareWallet(ctx, c)
				if err != nil {
					return err
				}

				opts := make([]nip60.SendOption, 0, 1)
				if mint := c.String("mint"); mint != "" {
					mint = "http" + nostr.NormalizeURL(mint)[2:]
					opts = append(opts, nip60.WithMint(mint))
				}

				preimage, err := w.PayBolt11(ctx, args[0], opts...)
				if err != nil {
					return err
				}

				stdout(preimage)

				closew()
				return nil
			},
		},
		{
			Name:                      "nutzap",
			Usage:                     "sends a nip61 nutzap to one or more Nostr profiles and/or events",
			ArgsUsage:                 "<amount> <target>",
			Description:               "<amount> is in satoshis, <target> can be an npub, nprofile, nevent or hex pubkey.",
			DisableSliceFlagSeparator: true,
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:  "mint",
					Usage: "send from a specific mint",
				},
				&cli.StringFlag{
					Name:  "message",
					Usage: "attach a message to the nutzap",
				},
			},
			Action: func(ctx context.Context, c *cli.Command) error {
				args := c.Args().Slice()
				if len(args) < 2 {
					return fmt.Errorf("must be called as `nak wallet nutzap <amount> <target>...")
				}

				w, closew, err := prepareWallet(ctx, c)
				if err != nil {
					return err
				}

				amount := c.Uint("amount")
				target := c.String("target")

				var evt *nostr.Event
				var eventId string

				if strings.HasPrefix(target, "nevent1") {
					evt, _, err = sys.FetchSpecificEventFromInput(ctx, target, sdk.FetchSpecificEventParameters{
						WithRelays: false,
					})
					if err != nil {
						return err
					}
					eventId = evt.ID
					target = evt.PubKey
				}

				pm, err := sys.FetchProfileFromInput(ctx, target)
				if err != nil {
					return err
				}

				log("sending %d sat to '%s' (%s)", amount, pm.ShortName(), pm.Npub())

				opts := make([]nip60.SendOption, 0, 1)
				if mint := c.String("mint"); mint != "" {
					mint = "http" + nostr.NormalizeURL(mint)[2:]
					opts = append(opts, nip60.WithMint(mint))
				}

				kr, _, _ := gatherKeyerFromArguments(ctx, c)
				results, err := nip61.SendNutzap(
					ctx,
					kr,
					w,
					sys.Pool,
					pm.PubKey,
					sys.FetchInboxRelays,
					sys.FetchOutboxRelays(ctx, pm.PubKey, 3),
					eventId,
					amount,
					c.String("message"),
				)
				if err != nil {
					return err
				}

				log("- publishing nutzap... ")
				first := true
				for res := range results {
					cleanUrl, _ := strings.CutPrefix(res.RelayURL, "wss://")
					if !first {
						log(", ")
					}
					first = false
					if res.Error != nil {
						log("%s: %s", colors.errorf(cleanUrl), res.Error)
					} else {
						log("%s: ok", colors.successf(cleanUrl))
					}
				}

				closew()
				return nil
			},
			Commands: []*cli.Command{
				{
					Name:                      "setup",
					Usage:                     "setup your wallet private key and kind:10019 event for receiving nutzaps",
					DisableSliceFlagSeparator: true,
					Flags: []cli.Flag{
						&cli.StringSliceFlag{
							Name:  "mint",
							Usage: "mints to receive nutzaps in",
						},
						&cli.StringFlag{
							Name:  "private-key",
							Usage: "private key used for receiving nutzaps",
						},
						&cli.BoolFlag{
							Name:    "force",
							Aliases: []string{"f"},
							Usage:   "forces replacement of private-key",
						},
					},
					Action: func(ctx context.Context, c *cli.Command) error {
						w, closew, err := prepareWallet(ctx, c)
						if err != nil {
							return err
						}

						if w.PrivateKey == nil {
							if sk := c.String("private-key"); sk != "" {
								if err := w.SetPrivateKey(ctx, sk); err != nil {
									return err
								}
							} else {
								return fmt.Errorf("missing --private-key")
							}
						} else if sk := c.String("private-key"); sk != "" && !c.Bool("force") {
							return fmt.Errorf("refusing to replace existing private key, use the --force flag")
						}

						kr, _, _ := gatherKeyerFromArguments(ctx, c)
						pk, _ := kr.GetPublicKey(ctx)
						relays := sys.FetchWriteRelays(ctx, pk, 6)

						info := nip61.Info{}
						ie := sys.Pool.QuerySingle(ctx, relays, nostr.Filter{
							Kinds:   []int{10019},
							Authors: []string{pk},
							Limit:   1,
						})
						if ie != nil {
							info.ParseEvent(ie.Event)
						}

						if mints := c.StringSlice("mints"); len(mints) == 0 && len(info.Mints) == 0 {
							info.Mints = w.Mints
						}
						if len(info.Mints) == 0 {
							return fmt.Errorf("missing --mint")
						}

						evt := nostr.Event{}
						if err := info.ToEvent(ctx, kr, &evt); err != nil {
							return err
						}

						stdout(evt)
						log("- saving kind:10019 event... ")
						first := true
						for res := range sys.Pool.PublishMany(ctx, relays, evt) {
							cleanUrl, _ := strings.CutPrefix(res.RelayURL, "wss://")

							if !first {
								log(", ")
							}
							first = false

							if res.Error != nil {
								log("%s: %s", colors.errorf(cleanUrl), res.Error)
							} else {
								log("%s: ok", colors.successf(cleanUrl))
							}
						}

						closew()
						return nil
					},
				},
			},
		},
	},
}
