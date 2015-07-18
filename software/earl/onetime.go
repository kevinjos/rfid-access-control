package main

import (
	"github.com/JustinJudd/passgen"
	"time"
)

const expirationWindow = time.Minute * 2

type Onetime struct {
	pass         string
	rfid         string
	authUserCode string
	expiration   time.Time
}

func NewOnetime(now time.Time, rfid, authUserCode string) *Onetime {
	pass := genPass()
	expire := now.Add(expirationWindow)
	return &Onetime{
		pass:         pass,
		rfid:         rfid,
		authUserCode: authUserCode,
		expiration:   expire,
	}
}

func genPass() string {
	pass, err := passgen.GetXKCDPassphrase(3)
	if len(pass) > maxLCDCols {
		pass = genPass()
	}
	if err != nil {
		return ""
	}
	return pass
}

func (o *Onetime) IsExpired(now time.Time) bool {
	return now.After(o.expiration)
}
