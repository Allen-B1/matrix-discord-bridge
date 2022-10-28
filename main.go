package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gomarkdown/markdown"
	mhtml "github.com/gomarkdown/markdown/html"
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

func fileSize(bytes int) string {
	if bytes >= 1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(1024*1024))
	} else if bytes >= 1024 {
		return fmt.Sprintf("%.1f kB", float64(bytes)/float64(1024))
	} else {
		return fmt.Sprint(bytes) + " B"
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

			filename := ""
			if f, ok := ev.Content["filename"].(string); ok {
				filename = f
			} else {
				filename = fmt.Sprint(ev.Content["body"])
			}

			if err == nil {
				_, err = dg.WebhookExecute(webhookId, webhookToken, false, &discordgo.WebhookParams{
					Files: []*discordgo.File{{
						Name:        filename,
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
		if roomID == "" {
			return
		}

		if m.Content != "" {
			contentHTML := string(markdown.ToHTML([]byte(m.Content), nil, mhtml.NewRenderer(mhtml.RendererOptions{Flags: mhtml.CommonFlags})))
			contentHTML = strings.TrimSpace(contentHTML)
			if strings.HasPrefix(contentHTML, "<p>") && strings.HasSuffix(contentHTML, "</p>") {
				contentHTML = strings.TrimPrefix(contentHTML, "<p>")
				contentHTML = strings.TrimSuffix(contentHTML, "</p>")
			}
			_, err := mg.SendFormattedText(roomID,
				m.Author.Username+": "+m.Content,
				"<b>"+m.Author.Username+"</b>: "+contentHTML)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error sending to `"+roomID+"` : ", err)
			}
		}

		if len(m.Message.Attachments) == 1 && m.Message.Attachments[0].Size <= 64*1024 {
			attachment := m.Attachments[0]
			upload, err := mg.UploadLink(attachment.URL)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error uploading attachment to `"+config.Bridge[m.ChannelID]+"` : ", err)
			}

			if strings.HasPrefix(attachment.ContentType, "image/") {
				_, err := mg.SendMessageEvent(roomID, "m.room.message", map[string]interface{}{
					"body":     m.Author.Username + " uploaded " + attachment.Filename,
					"filename": attachment.Filename,
					"msgtype":  "m.image",
					"url":      upload.ContentURI,
					"info": map[string]interface{}{
						"mimetype": attachment.ContentType,
						"size":     attachment.Size,
					},
				})
				if err != nil {
					fmt.Fprintln(os.Stderr, "error sending attachment to `"+config.Bridge[m.ChannelID]+"` : ", err)
				}
			} else {
				mg.SendMessageEvent(roomID, "m.room.message", map[string]interface{}{
					"body":     m.Author.Username + " uploaded " + attachment.Filename,
					"filename": attachment.Filename,
					"msgtype":  "m.file",
					"url":      upload.ContentURI,
					"info": map[string]interface{}{
						"mimetype": attachment.ContentType,
						"size":     attachment.Size,
					},
				})
			}
		} else if len(m.Attachments) != 0 {
			contentPlain := m.Author.Username + " uploaded files"
			contentHTML := "<b>" + m.Author.Username + "</b> uploaded files<table><tr><th>Link</th><th>MIME Type</th><th>Size</th></tr>"
			for _, attachment := range m.Message.Attachments {
				contentHTML += fmt.Sprintf("<tr><td><a href=\"%s\">%s</a></td><td>%s</td><td>%s</td></tr>", attachment.URL, html.EscapeString(attachment.Filename), attachment.ContentType, fileSize(attachment.Size))
				contentPlain += fmt.Sprintf("\n%s (%s): %s", attachment.Filename, fileSize(attachment.Size), attachment.URL)
			}
			contentHTML += "</table>"

			_, err := mg.SendFormattedText(roomID, contentPlain, contentHTML)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error uploading file table", err)
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
