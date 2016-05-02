package server

import (
	"net/http"
	"time"

	"github.com/drone/drone/model"
	"github.com/drone/drone/remote"
	"github.com/drone/drone/shared/crypto"
	"github.com/drone/drone/shared/httputil"
	"github.com/drone/drone/shared/token"
	"github.com/drone/drone/store"

	"github.com/Sirupsen/logrus"
	"github.com/gin-gonic/gin"
)

func GetLogin(c *gin.Context) {

	// when dealing with redirects we may need to adjust the content type. I
	// cannot, however, remember why, so need to revisit this line.
	c.Writer.Header().Del("Content-Type")

	tmpuser, err := remote.Login(c, c.Writer, c.Request)
	if err != nil {
		logrus.Errorf("cannot authenticate user. %s", err)
		c.Redirect(303, "/login?error=oauth_error")
		return
	}
	// this will happen when the user is redirected by the remote provider as
	// part of the authorization workflow.
	if tmpuser == nil {
		return
	}
	config := ToConfig(c)

	// get the user from the database
	u, err := store.GetUserLogin(c, tmpuser.Login)
	if err != nil {

		// if self-registration is disabled we should return a not authorized error
		if !config.Open {
			logrus.Errorf("cannot register %s. registration closed", tmpuser.Login)
			c.Redirect(303, "/login?error=access_denied")
			return
		}

		// create the user account
		u = &model.User{
			Login:  tmpuser.Login,
			Token:  tmpuser.Token,
			Secret: tmpuser.Secret,
			Email:  tmpuser.Email,
			Avatar: tmpuser.Avatar,
			Hash:   crypto.Rand(),
		}

		// insert the user into the database
		if err := store.CreateUser(c, u); err != nil {
			logrus.Errorf("cannot insert %s. %s", u.Login, err)
			c.Redirect(303, "/login?error=internal_error")
			return
		}
	}

	// update the user meta data and authorization data.
	u.Token = tmpuser.Token
	u.Secret = tmpuser.Secret
	u.Email = tmpuser.Email
	u.Avatar = tmpuser.Avatar

	if err := store.UpdateUser(c, u); err != nil {
		logrus.Errorf("cannot update %s. %s", u.Login, err)
		c.Redirect(303, "/login?error=internal_error")
		return
	}

	if len(config.Orgs) != 0 {
		teams, terr := remote.Teams(c, u)
		if terr != nil {
			logrus.Errorf("cannot verify team membership for %s. %s.", tmpuser.Login, terr)
			c.Redirect(303, "/login?error=access_denied")
			return
		}
		var member bool
		for _, team := range teams {
			if config.Orgs[team.Login] {
				member = true
				break
			}
		}
		if !member {
			logrus.Errorf("cannot verify team membership for %s. %s.", tmpuser.Login, terr)
			c.Redirect(303, "/login?error=access_denied")
			return
		}
	}

	exp := time.Now().Add(time.Hour * 72).Unix()
	token := token.New(token.SessToken, u.Login)
	tokenstr, err := token.SignExpires(u.Hash, exp)
	if err != nil {
		logrus.Errorf("cannot create token for %s. %s", u.Login, err)
		c.Redirect(303, "/login?error=internal_error")
		return
	}

	httputil.SetCookie(c.Writer, c.Request, "user_sess", tokenstr)
	redirect := httputil.GetCookie(c.Request, "user_last")
	if len(redirect) == 0 {
		redirect = "/"
	}
	c.Redirect(303, redirect)

}

func GetLogout(c *gin.Context) {
	httputil.DelCookie(c.Writer, c.Request, "user_sess")
	httputil.DelCookie(c.Writer, c.Request, "user_last")
	c.Redirect(303, "/login")
}

func GetLoginToken(c *gin.Context) {
	in := &tokenPayload{}
	err := c.Bind(in)
	if err != nil {
		c.AbortWithError(http.StatusBadRequest, err)
		return
	}

	login, err := remote.Auth(c, in.Access, in.Refresh)
	if err != nil {
		c.AbortWithError(http.StatusUnauthorized, err)
		return
	}

	user, err := store.GetUserLogin(c, login)
	if err != nil {
		c.AbortWithError(http.StatusNotFound, err)
		return
	}

	exp := time.Now().Add(time.Hour * 72).Unix()
	token := token.New(token.SessToken, user.Login)
	tokenstr, err := token.SignExpires(user.Hash, exp)
	if err != nil {
		c.AbortWithError(http.StatusInternalServerError, err)
		return
	}

	c.IndentedJSON(http.StatusOK, &tokenPayload{
		Access:  tokenstr,
		Expires: exp - time.Now().Unix(),
	})
}

type tokenPayload struct {
	Access  string `json:"access_token,omitempty"`
	Refresh string `json:"refresh_token,omitempty"`
	Expires int64  `json:"expires_in,omitempty"`
}

// ToConfig returns the config from the Context
func ToConfig(c *gin.Context) *model.Config {
	v := c.MustGet("config")
	return v.(*model.Config)
}