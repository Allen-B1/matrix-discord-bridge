package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/matrix-org/gomatrix"
)

type MatrixConfig struct {
	Homeserver  string `json:"homeserver"`
	Username    string `json:"username"`
	AccessToken string `json:"access_token"`
}

type DiscordConfig struct {
	Token  string `json:"token"`
	Prefix string `json:"prefix"`
}

type Config struct {
	Matrix MatrixConfig `json:"matrix"`

	Discord DiscordConfig `json:"discord"`

	// A map from discord channel ID to matrix room ID
	Bridge map[string]string `json:"bridge"`
}

func writeDefaultConfig(configPath string) {
	defaultConfig := Config{
		Matrix: MatrixConfig{
			Homeserver:  "https://matrix-client.matrix.org",
			Username:    "@username:matrix.org",
			AccessToken: "access.token",
		},
		Discord: DiscordConfig{
			Token:  "some.bot.token",
			Prefix: ":",
		},
		Bridge: map[string]string{
			"1235678930234": "@room:matrix.org",
		},
	}

	configFile, err := os.Create(configPath)
	if err != nil {
		panic(err)
	}
	defer configFile.Close()
	bytes, err := json.MarshalIndent(defaultConfig, "", "\t")
	if err != nil {
		panic(err)
	}
	configFile.Write(bytes)
}

// Strip a matrix username
func stripMatrixName(username string) string {
	if idx := strings.Index(username, ":"); idx != -1 {
		username = username[0:idx]
	}
	username = strings.TrimPrefix(username, "@")
	return username
}

func getContent(config *Config, uri string) (io.Reader, error) {
	if strings.HasPrefix(uri, "mxc://") {
		resp, err := http.Get(config.Matrix.Homeserver + "/_matrix/media/r0/download/" + uri[6:])
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	} else {
		resp, err := http.Get(uri)
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	}
}

var configPath string

func init() {
	flag.StringVar(&configPath, "config", "config.json", "Path to configuration file")
}

func main() {
	flag.Parse()

	configFile, err := os.Open(configPath)
	if err != nil {
		writeDefaultConfig(configPath)
		return
	}
	defer configFile.Close()

	configData, err := io.ReadAll(configFile)
	if err != nil {
		panic("can't read config file: " + err.Error())
	}
	var config Config
	err = json.Unmarshal(configData, &config)
	if err != nil {
		panic("invalid configuration file: " + err.Error())
	}
	var matrixToDiscord = make(map[string]string)
	for disc, matrix := range config.Bridge {
		matrixToDiscord[matrix] = disc
	}

	// initialize matrix & discord connections
	mg, err := gomatrix.NewClient(config.Matrix.Homeserver, config.Matrix.Username, config.Matrix.AccessToken)
	if err != nil {
		panic("error connecting to matrix: " + err.Error())
	}
	startTime := time.Now().UnixMilli()

	dg, err := discordgo.New("Bot " + config.Discord.Token)
	dg.Identify.Intents |= discordgo.IntentMessageContent
	if err != nil {
		panic("error connecting to discord: " + err.Error())
	}
	err = dg.Open()
	if err != nil {
		panic("error connecting to discord: " + err.Error())
	}

	webhooks, err := NewWebhookManager(dg, ".webhooks")
	if err != nil {
		panic("error creating webhook manager: " + err.Error())
	}

	// handle events
	syncer := mg.Syncer.(*gomatrix.DefaultSyncer)
	syncer.OnEventType("m.room.message", func(ev *gomatrix.Event) {
		if ev.Timestamp < startTime {
			return
		}

		if ev.Sender == config.Matrix.Username {
			return
		}

		discordId := matrixToDiscord[ev.RoomID]
		if discordId == "" {
			return
		}

		webhookId, webhookToken, err := webhooks.Get(discordId, ev.Sender)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error getting webhook: ", err)
		}

		switch fmt.Sprint(ev.Content["msgtype"]) {
		case "m.text", "m.notice":
			_, err = dg.WebhookExecute(webhookId, webhookToken, false, &discordgo.WebhookParams{
				Content:  fmt.Sprint(ev.Content["body"]),
				Username: stripMatrixName(ev.Sender)})
			if err != nil {
				fmt.Fprintln(os.Stderr, "error sending webhook: ", err)
			}
		case "m.emote":
			_, err = dg.ChannelMessageSend(discordId, "* **"+stripMatrixName(ev.Sender)+"** "+fmt.Sprint(ev.Content["body"]))
			if err != nil {
				fmt.Fprintln(os.Stderr, "error sending emote to discord: ", err)
			}

		case "m.image", "m.audio", "m.video":
			mimeType := fmt.Sprint(ev.Content["info"].(map[string]interface{})["mimetype"])
			extensions, err := mime.ExtensionsByType(mimeType)
			extension := ""
			if err == nil && len(extensions) != 0 {
				extension = extensions[0]
			}
			reader, err := getContent(&config, fmt.Sprint(ev.Content["url"]))
			if err != nil {
				fmt.Fprintln(os.Stderr, "error reading image/audio/video: ", err)
			}
			_, err = dg.WebhookExecute(webhookId, webhookToken, false, &discordgo.WebhookParams{
				Files: []*discordgo.File{{
					Name:        fmt.Sprint(ev.Content["msgtype"])[2:] + extension,
					ContentType: mimeType,
					Reader:      reader,
				}},
				Username: stripMatrixName(ev.Sender)})
			if err != nil {
				fmt.Fprintln(os.Stderr, "error sending webhook: ", err)
			}
		case "m.file":
			reader, err := getContent(&config, fmt.Sprint(ev.Content["url"]))
			if err != nil {
				fmt.Fprintln(os.Stderr, "error reading file: ", err)
			}
			if err == nil {
				_, err = dg.WebhookExecute(webhookId, webhookToken, false, &discordgo.WebhookParams{
					Files: []*discordgo.File{{
						Name:        fmt.Sprint(ev.Content["filename"]),
						ContentType: fmt.Sprint(ev.Content["info"].(map[string]interface{})["mimetype"]),
						Reader:      reader,
					}},
					Username: stripMatrixName(ev.Sender)})
				if err != nil {
					fmt.Fprintln(os.Stderr, "error sending webhook: ", err)
				}
			}
		}
	})
	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.ID == dg.State.User.ID || webhooks.Has(m.Author.ID) {
			return
		}

		roomID := config.Bridge[m.ChannelID]
		if roomID != "" {
			if m.Content != "" {
				_, err := mg.SendFormattedText(config.Bridge[m.ChannelID], m.Author.Username+": "+m.Content, "<b>"+m.Author.Username+"</b>: "+m.Content)
				if err != nil {
					fmt.Fprintln(os.Stderr, "error sending to `"+config.Bridge[m.ChannelID]+"` : ", err)
				}
			}
			if len(m.Message.Attachments) != 0 {
				for _, attachment := range m.Message.Attachments {
					upload, err := mg.UploadLink(attachment.URL)
					if err != nil {
						fmt.Fprintln(os.Stderr, "error uploading attachment to `"+config.Bridge[m.ChannelID]+"` : ", err)
					}

					if strings.HasPrefix(attachment.ContentType, "image/") {
						_, err = mg.SendImage(roomID, attachment.Filename, upload.ContentURI)
						if err != nil {
							fmt.Fprintln(os.Stderr, "error sending attachment to `"+config.Bridge[m.ChannelID]+"` : ", err)
						}
					} else {
						mg.SendMessageEvent(roomID, "m.room.message", map[string]interface{}{
							"body":     attachment.Filename,
							"filename": attachment.Filename,
							"msgtype":  "m.file",
							"url":      upload.ContentURI,
							"info": map[string]interface{}{
								"mimetype": attachment.ContentType,
								"size":     attachment.Size,
							},
						})
					}
				}
			}
		}
	})

	fmt.Println("Discord: " + dg.State.User.Username + "#" + dg.State.User.Discriminator)
	fmt.Println("Matrix: " + mg.UserID)

	for {
		if err := mg.Sync(); err != nil {
			fmt.Fprintln(os.Stderr, "sync error: ", err)
		}

		time.Sleep(time.Second * 1)
	}
}
