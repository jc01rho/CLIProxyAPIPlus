package trae

// UserJWT represents the JWT token information returned by Trae.
type UserJWT struct {
	ClientID            string `json:"ClientID"`
	RefreshToken        string `json:"RefreshToken"`
	RefreshExpireAt     int64  `json:"RefreshExpireAt"`     // Unix ms
	Token               string `json:"Token"`               // JWT
	TokenExpireAt       int64  `json:"TokenExpireAt"`       // Unix ms
	TokenExpireDuration int64  `json:"TokenExpireDuration"` // 14 days in ms
}

// UserInfo represents the user profile information returned by Trae.
type UserInfo struct {
	ScreenName   string `json:"ScreenName"`
	Gender       string `json:"Gender"`
	AvatarUrl    string `json:"AvatarUrl"`
	UserID       string `json:"UserID"`
	Description  string `json:"Description"`
	TenantID     string `json:"TenantID"`
	RegisterTime int64  `json:"RegisterTime"`
}

// NativeAuthParams represents the parameters required to generate the Trae native OAuth URL.
type NativeAuthParams struct {
	LoginVersion    string `json:"login_version"`
	AuthFrom        string `json:"auth_from"`
	LoginChannel    string `json:"login_channel"`
	PluginVersion   string `json:"plugin_version"`
	AuthType        string `json:"auth_type"`
	ClientID        string `json:"client_id"`
	Redirect        string `json:"redirect"`
	LoginTraceID    string `json:"login_trace_id"`
	AuthCallbackURL string `json:"auth_callback_url"`
	MachineID       string `json:"machine_id"`
	DeviceID        string `json:"device_id"`
	XDeviceID       string `json:"x_device_id"`
	XMachineID      string `json:"x_machine_id"`
	XDeviceBrand    string `json:"x_device_brand"`
	XDeviceType     string `json:"x_device_type"`
	XOSVersion      string `json:"x_os_version"`
	XEnv            string `json:"x_env"`
	XAppVersion     string `json:"x_app_version"`
	XAppType        string `json:"x_app_type"`
}

// NativeCallbackResult represents the result received from the Trae native OAuth callback.
type NativeCallbackResult struct {
	IsRedirect      string `json:"isRedirect"`
	Scope           string `json:"scope"`
	Data            string `json:"data"`
	RefreshToken    string `json:"refreshToken"`
	LoginTraceID    string `json:"loginTraceID"`
	Host            string `json:"host"`
	RefreshExpireAt string `json:"refreshExpireAt"`
	UserRegion      string `json:"userRegion"`
	UserJWT         string `json:"userJwt"`  // JSON string
	UserInfo        string `json:"userInfo"` // JSON string
}
