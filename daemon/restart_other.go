//go:build !windows

package daemon

func ValidateRestart(string) error                                            { return nil }
func PrepareRestart(string, string, string, string) (*PreparedRestart, error) { return nil, nil }
func ConfirmRestartReady(string, string, string) error                        { return nil }
func RunRestartController(string) error                                       { return nil }
func Supervised() bool                                                        { return false }
