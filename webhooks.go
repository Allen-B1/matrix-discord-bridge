package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	_ "embed"

	"github.com/bwmarrin/discordgo"
)

type WebhookInfo struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

type WebhookManager struct {
	dg       *discordgo.Session
	webhooks map[string]WebhookInfo
	file     string

	lock sync.RWMutex
}

func NewWebhookManager(dg *discordgo.Session, file string) (*WebhookManager, error) {
	webhooks := make(map[string]WebhookInfo)
	bytes, err := os.ReadFile(file)
	if err == nil {
		err = json.Unmarshal(bytes, &webhooks)
		if err != nil {
			return nil, err
		}
	}

	return &WebhookManager{dg: dg, file: file, webhooks: webhooks}, nil
}

//go:embed assets/webhook-discord.txt
var webhookAvatarDiscord string

// Get a discord webhook ID for a matrix username.
// Creates a webhook if it does not exist.
func (m *WebhookManager) Get(channel string, username string) (string, string, error) {
	m.lock.RLock()
	webhook, ok := m.webhooks[channel+" | "+username]
	m.lock.RUnlock()

	if !ok {
		webhookObj, err := m.dg.WebhookCreate(channel, username, "data:image/png;base64,"+strings.ReplaceAll(webhookAvatarDiscord, "\n", ""))
		if err != nil {
			return "", "", err
		}

		webhook = WebhookInfo{ID: webhookObj.ID, Token: webhookObj.Token}
		m.lock.Lock()
		m.webhooks[channel+" | "+username] = webhook
		m.lock.Unlock()
		if err = m.save(); err != nil {
			fmt.Fprintf(os.Stderr, "error saving webhooks: "+err.Error())
		}
	}

	return webhook.ID, webhook.Token, nil
}

func (m *WebhookManager) Has(id string) bool {
	m.lock.RLock()
	defer m.lock.RUnlock()
	for _, webhook := range m.webhooks {
		if webhook.ID == id {
			return true
		}
	}
	return false
}

func (m *WebhookManager) save() error {
	bytes, err := json.Marshal(m.webhooks)
	if err != nil {
		return err
	}
	return os.WriteFile(m.file, bytes, os.ModePerm)
}
