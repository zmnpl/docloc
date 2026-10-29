package ui

import "github.com/charmbracelet/lipgloss"

var (
	accent = lipgloss.AdaptiveColor{Light: "#5A3FC0", Dark: "#A78BFA"}
	dim    = lipgloss.AdaptiveColor{Light: "#8A8A8A", Dark: "#6C6C6C"}
	subtle = lipgloss.AdaptiveColor{Light: "#C8C8C8", Dark: "#3A3A3A"}

	promptStyle       = lipgloss.NewStyle().Foreground(accent).Bold(true)
	modeStyle         = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1E1E1E"}).Background(accent)
	statusStyle       = lipgloss.NewStyle().Foreground(dim)
	warnStyle         = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#F59E0B"})
	dimStyle          = lipgloss.NewStyle().Foreground(dim)
	dirStyle          = lipgloss.NewStyle().Foreground(dim)
	nameStyle         = lipgloss.NewStyle()
	cursorStyle       = lipgloss.NewStyle().Foreground(accent).Bold(true)
	selectedStyle     = lipgloss.NewStyle().Bold(true).Background(lipgloss.AdaptiveColor{Light: "#ECE8FB", Dark: "#2E2A3D"})
	borderStyle       = lipgloss.NewStyle().Foreground(subtle)
	previewTitleStyle = lipgloss.NewStyle().Foreground(accent)
	textStyle         = lipgloss.NewStyle()
	matchStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#000000", Dark: "#1E1E1E"}).Background(lipgloss.AdaptiveColor{Light: "#FDE68A", Dark: "#FACC15"})
	helpStyle         = lipgloss.NewStyle().Foreground(dim)
)
