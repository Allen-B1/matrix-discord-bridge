package main

import (
	"encoding/json"
	"log"
	"os"
	"sync"
)

type MessageInfo struct {
	DiscordID    string `json:"discord_id"`
	WebhookID    string `json:"webhook_id"`
	WebhookToken string `json:"webhook_token"`
	ChannelID    string `json:"channel_id"`
	GuildID      string `json:"guild_id"`

	RoomID   string `json:"room_id"`
	MatrixID string `json:"matrix_id"`

	// Discord properties
	Content   string `json:"content"`
	Author    string `json:"author"`
	AvatarURL string `json:"avatar_url"`
}

type MessageManager struct {
	file string

	// discord message ID => $matrix event ID
	discordToMessage map[string]*MessageInfo
	matrixToMessage  map[string]*MessageInfo

	lock sync.RWMutex
}

func NewMessageManager(file string) (*MessageManager, error) {
	body, err := os.ReadFile(file)
	if err != nil {
		return &MessageManager{
			file:             file,
			discordToMessage: make(map[string]*MessageInfo),
			matrixToMessage:  make(map[string]*MessageInfo),
		}, nil
	}

	messageList := make(map[string]*MessageInfo)
	err = json.Unmarshal(body, &messageList)
	if err != nil {
		return nil, err
	}

	discordToMessage := make(map[string]*MessageInfo, len(messageList))
	matrixToMessage := make(map[string]*MessageInfo, len(messageList))
	for _, messageInfo := range messageList {
		discordToMessage[messageInfo.DiscordID] = messageInfo
		matrixToMessage[messageInfo.MatrixID] = messageInfo
	}

	return &MessageManager{file: file, discordToMessage: discordToMessage, matrixToMessage: matrixToMessage}, nil
}

func (m *MessageManager) save() {
	body, err := json.Marshal(m.discordToMessage)
	if err != nil {
		log.Printf("error marshalling messages: %v\n", err)
		return
	}

	os.WriteFile(m.file, body, os.ModePerm)
}

func (m *MessageManager) Add(msg *MessageInfo) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.discordToMessage[msg.DiscordID] = msg
	m.matrixToMessage[msg.MatrixID] = msg
	m.save()
}

func (m *MessageManager) GetDiscord(discordID string) *MessageInfo {
	m.lock.RLock()
	msg := m.discordToMessage[discordID]
	m.lock.RUnlock()
	return msg
}

func (m *MessageManager) GetMatrix(matrixID string) *MessageInfo {
	m.lock.RLock()
	msg := m.matrixToMessage[matrixID]
	m.lock.RUnlock()
	return msg
}
