package util

var KnownTwoPartTLDs = map[string]bool{
	"co.uk": true, "com.au": true, "co.jp": true, "co.nz": true, "com.br": true,
	"org.uk": true, "gov.uk": true, "ac.uk": true, "com.cn": true, "net.cn": true,
	"org.cn": true, "co.za": true, "com.es": true, "com.mx": true, "com.sg": true,
	"co.in": true, "ltd.uk": true, "plc.uk": true, "net.au": true, "org.au": true,
	"com.pa": true, "net.pa": true, "org.pa": true, "edu.pa": true, "gob.pa": true,
	"com.py": true, "net.py": true, "org.py": true, "edu.py": true, "gov.py": true,
}
