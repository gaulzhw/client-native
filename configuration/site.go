// Copyright 2019 HAProxy Technologies
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this files except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package configuration

import (
	"fmt"
	"reflect"
	"strconv"

	strfmt "github.com/go-openapi/strfmt"
	"github.com/haproxytech/client-native/misc"
	parser "github.com/haproxytech/config-parser"
	"github.com/haproxytech/models"
)

// GetSites returns configuration version and an array of
// configured sites. Returns error on fail.
func (c *Client) GetSites(transactionID string) (int64, models.Sites, error) {
	p, err := c.GetParser(transactionID)
	if err != nil {
		return 0, nil, err
	}

	sites, err := c.parseSites(p)
	if err != nil {
		return 0, nil, err
	}

	v, err := c.GetVersion(transactionID)
	if err != nil {
		return 0, nil, err
	}

	return v, sites, nil
}

// GetSite returns configuration version and a requested site.
// Returns error on fail or if backend does not exist.
func (c *Client) GetSite(name string, transactionID string) (int64, *models.Site, error) {
	p, err := c.GetParser(transactionID)
	if err != nil {
		return 0, nil, err
	}

	if !c.checkSectionExists(parser.Frontends, name, p) {
		return 0, nil, NewConfError(ErrObjectDoesNotExist, fmt.Sprintf("Site %s does not exist", name))
	}

	site := c.parseSite(name, p)
	if site == nil {
		return 0, nil, NewConfError(ErrObjectDoesNotExist, fmt.Sprintf("Site %s does not exist", name))
	}

	v, err := c.GetVersion(transactionID)
	if err != nil {
		return 0, nil, err
	}

	return v, site, nil
}

// CreateSite creates a site in configuration. One of version or transactionID is
// mandatory. Returns error on fail, nil on success.
func (c *Client) CreateSite(data *models.Site, transactionID string, version int64) error {
	var res []error
	var err error

	if c.UseValidation {
		validationErr := data.Validate(strfmt.Default)
		if validationErr != nil {
			return NewConfError(ErrValidationError, validationErr.Error())
		}
	}
	// start an implicit transaction for create site (multiple operations required) if not already given
	p, t, err := c.loadDataForChange(transactionID, version)
	if err != nil {
		return err
	}

	//create frontend
	frontend := serializeServiceToFrontend(data.Service, data.Name)

	if frontend != nil {
		err = c.CreateFrontend(frontend, t, 0)
		if err != nil {
			res = append(res, err)
		}
	}

	//create listeners
	for _, l := range data.Service.Listeners {
		//sanitize name
		if l.Name == "" {
			l.Name = l.Address + ":" + strconv.FormatInt(*l.Port, 10)
		}
		err = c.CreateBind(data.Name, l, t, 0)
		if err != nil {
			res = append(res, err)
		}
	}

	//create backends
	for _, b := range data.Farms {
		backend := serializeFarmToBackend(b)
		if backend == nil {
			continue
		}
		err = c.CreateBackend(backend, t, 0)
		if err != nil {
			res = append(res, err)
		}
		//create servers
		for _, s := range b.Servers {
			//sanitize name
			if s.Name == "" {
				s.Name = s.Address + ":" + strconv.FormatInt(*s.Port, 10)
			}
			err = c.CreateServer(b.Name, s, t, 0)
			if err != nil {
				res = append(res, err)
			}
		}
		//create bck-frontend relations
		err = c.createBckFrontendRels(data.Name, b, false, t, p)
		if err != nil {
			res = append(res, err)
		}
	}
	if len(res) > 0 {
		return c.handleError(data.Name, "", "", t, transactionID == "", CompositeTransactionError(res...))
	}

	if err := c.saveData(p, t, transactionID == ""); err != nil {
		return err
	}

	return nil
}

// EditSite edits a site in configuration. One of version or transactionID is
// mandatory. Returns error on fail, nil on success.
func (c *Client) EditSite(name string, data *models.Site, transactionID string, version int64) error {
	var res []error
	var err error

	if c.UseValidation {
		validationErr := data.Validate(strfmt.Default)
		if validationErr != nil {
			return NewConfError(ErrValidationError, validationErr.Error())
		}
	}
	// start an implicit transaction for create site (multiple operations required) if not already given
	p, t, err := c.loadDataForChange(transactionID, version)
	if err != nil {
		return err
	}

	_, site, err := c.GetSite(name, transactionID)
	if err != nil {
		return err
	}
	confS := site

	//edit frontend
	if !reflect.DeepEqual(data.Service, confS.Service) {
		err := c.editService(data.Name, data.Service, t, p)
		if err != nil {
			res = append(res, err)
		}
		//compare listeners
		if !reflect.DeepEqual(confS.Service.Listeners, data.Service.Listeners) {
			//add missing listeners by name, edit existing
			for _, l := range data.Service.Listeners {
				found := false
				for _, confL := range confS.Service.Listeners {
					if l.Name == confL.Name {
						if !reflect.DeepEqual(l, confL) {
							err := c.EditBind(l.Name, data.Name, l, t, 0)
							if err != nil {
								res = append(res, err)
							}
						}
						found = true
						break
					}
				}
				if !found {
					//sanitize name
					if l.Name == "" {
						l.Name = l.Address + ":" + strconv.FormatInt(*l.Port, 10)
					}
					err = c.CreateBind(data.Name, l, t, 0)
					if err != nil {
						res = append(res, err)
					}
				}
			}
			//delete non existing listeners
			for _, confL := range confS.Service.Listeners {
				found := false
				for _, l := range data.Service.Listeners {
					if l.Name == confL.Name {
						found = true
						break
					}
				}
				if !found {
					err = c.DeleteBind(confL.Name, data.Name, t, 0)
					if err != nil {
						res = append(res, err)
					}
				}
			}
		}
	}
	bcks := make([]interface{}, len(confS.Farms))
	for i := range confS.Farms {
		bcks[i] = confS.Farms[i]
	}
	defaultBck := ""
	// check if backends changed
	if !reflect.DeepEqual(confS.Farms, data.Farms) {
		for _, b := range data.Farms {
			// add missing backends
			confBIface := misc.GetObjByField(bcks, "Name", b.Name)
			if confBIface == nil {
				backend := serializeFarmToBackend(b)
				if b != nil {
					err = c.CreateBackend(backend, t, 0)
					if err != nil {
						res = append(res, err)
					}
					for _, s := range b.Servers {
						err := c.CreateServer(b.Name, s, t, 0)
						if err != nil {
							res = append(res, err)
						}
					}
					if b.UseAs == "default" && defaultBck != "" {
						return NewConfError(ErrValidationError, fmt.Sprintf("Multiple default backends found in site: %v", name))
					} else if b.UseAs == "default" && defaultBck == "" {
						defaultBck = b.Name
					}
					//create bck-frontend relations
					err = c.createBckFrontendRels(name, b, false, t, p)
					if err != nil {
						res = append(res, err)
					}
				}
			} else {
				if b.UseAs == "default" && defaultBck != "" {
					return NewConfError(ErrValidationError, fmt.Sprintf("Multiple default backends found in site: %v", name))
				} else if b.UseAs == "default" && defaultBck == "" {
					defaultBck = b.Name
				}
				confB := confBIface.(*models.SiteFarm)
				if !reflect.DeepEqual(b, confB) {
					// check if use as has changed
					if b.UseAs != confB.UseAs {
						err := c.createBckFrontendRels(name, b, true, t, p)
						if err != nil {
							res = append(res, err)
						}
					}
					err := c.editFarm(b.Name, b, t, p)
					if err != nil {
						res = append(res, err)
					}
					for _, srv := range b.Servers {
						found := false
						for _, confSrv := range confB.Servers {
							if srv.Name == confSrv.Name {
								if !reflect.DeepEqual(srv, confSrv) {
									err := c.EditServer(srv.Name, b.Name, srv, t, 0)
									if err != nil {
										res = append(res, err)
									}
								}
								found = true
								break
							}
						}
						if !found {
							err = c.CreateServer(b.Name, srv, t, 0)
							if err != nil {
								res = append(res, err)
							}
						}
					}
					//delete non existing servers
					for _, confSrv := range confB.Servers {
						found := false
						for _, srv := range b.Servers {
							if srv.Name == confSrv.Name {
								found = true
								break
							}
						}
						if !found {
							err = c.DeleteServer(confSrv.Name, b.Name, t, 0)
							if err != nil {
								res = append(res, err)
							}
						}
					}
				}
			}
		}
		bcks = make([]interface{}, len(data.Farms))
		for i := range data.Farms {
			bcks[i] = data.Farms[i]
		}
		// delete non existing backends and remove uses in frontends
		for _, b := range confS.Farms {
			if misc.GetObjByField(bcks, "Name", b.Name) == nil {
				// default_bck
				if b.UseAs == "conditional" {
					// find the correct usefarm and remove it
					err := c.removeUseFarm(name, b.Name, t, p)
					if err != nil {
						res = append(res, err)
					}
				}
				err := c.DeleteBackend(b.Name, t, 0)
				if err != nil {
					res = append(res, err)
				}
			}
		}
	}
	// remove default backend if no default backends specified
	if defaultBck == "" {
		err = c.removeDefaultBckToFrontend(name, t, p)
		if err != nil {
			res = append(res, err)
		}
	}

	if len(res) > 0 {
		return c.handleError(data.Name, "", "", t, transactionID == "", CompositeTransactionError(res...))
	}

	if err := c.saveData(p, t, transactionID == ""); err != nil {
		return err
	}

	return nil
}

// DeleteSite deletes a site in configuration. One of version or transactionID is
// mandatory. Returns error on fail, nil on success.
func (c *Client) DeleteSite(name string, transactionID string, version int64) error {
	var res []error
	var err error

	// start an implicit transaction for delete site (multiple operations required) if not already given
	p, t, err := c.loadDataForChange(transactionID, version)
	if err != nil {
		return err
	}

	_, site, err := c.GetSite(name, t)
	if err != nil {
		return err
	}

	err = c.DeleteFrontend(site.Name, t, 0)
	if err != nil {
		res = append(res, err)
	}

	for _, b := range site.Farms {
		err = c.DeleteBackend(b.Name, t, 0)
		if err != nil {
			res = append(res, err)
		}
	}

	if len(res) > 0 {
		return c.handleError(name, "", "", t, transactionID == "", CompositeTransactionError(res...))
	}

	if err := c.saveData(p, t, transactionID == ""); err != nil {
		return err
	}

	return nil
}

func (c *Client) parseSites(p *parser.Parser) (models.Sites, error) {
	sites := models.Sites{}
	fNames, err := p.SectionsGet(parser.Frontends)
	if err != nil {
		return nil, err
	}

	for _, s := range fNames {
		site := c.parseSite(s, p)
		if site != nil {
			sites = append(sites, site)
		}
	}
	return sites, nil
}

func (c *Client) parseSite(s string, p *parser.Parser) *models.Site {
	frontend := &models.Frontend{Name: s}
	if err := c.parseSection(frontend, parser.Frontends, s, p); err != nil {
		return nil
	}

	ls, _ := c.parseBinds(s, p)
	site := &models.Site{
		Name: s,
		Service: &models.SiteService{
			HTTPConnectionMode: frontend.HTTPConnectionMode,
			Maxconn:            frontend.Maxconn,
			Mode:               frontend.Mode,
			Listeners:          ls,
		},
		Farms: []*models.SiteFarm{},
	}

	// Find backends using default_backend and use_backends
	if frontend.DefaultBackend != "" {
		// parse default backend
		farm := c.parseFarm(frontend.DefaultBackend, "default", "", "", p)
		if farm != nil {
			site.Farms = append(site.Farms, farm)
		}
	}
	ubs, err := c.parseBackendSwitchingRules(s, p)
	if err == nil {
		for _, ub := range ubs {
			farm := c.parseFarm(ub.Name, "conditional", ub.Cond, ub.CondTest, p)
			if farm != nil {
				site.Farms = append(site.Farms, farm)
			}
		}
	}
	return site
}

func (c *Client) parseFarm(name string, useAs string, cond string, condTest string, p *parser.Parser) *models.SiteFarm {
	backend := &models.Backend{Name: name}
	if err := c.parseSection(backend, parser.Backends, name, p); err == nil {
		srvs, err := c.parseServers(name, p)
		if err != nil {
			srvs = models.Servers{}
		}
		farm := &models.SiteFarm{
			UseAs:      useAs,
			Cond:       cond,
			CondTest:   condTest,
			Mode:       backend.Mode,
			Name:       backend.Name,
			Forwardfor: backend.Forwardfor,
			Balance:    backend.Balance,
			Servers:    srvs,
		}
		return farm
	}
	return nil
}

func serializeServiceToFrontend(service *models.SiteService, name string) *models.Frontend {
	fr := &models.Frontend{Name: name}
	if service != nil {
		fr.Mode = service.Mode
		fr.Maxconn = service.Maxconn
		fr.HTTPConnectionMode = service.HTTPConnectionMode

	}
	return fr
}

func serializeFarmToBackend(farm *models.SiteFarm) *models.Backend {
	return &models.Backend{
		Name:       farm.Name,
		Mode:       farm.Mode,
		Forwardfor: farm.Forwardfor,
		Balance:    farm.Balance,
	}
}

// frontend backend relation helper methods
func (c *Client) removeUseFarm(frontend string, backend string, t string, p *parser.Parser) error {
	ufs, err := c.parseBackendSwitchingRules(frontend, p)
	if err != nil {
		return err
	}
	for i, uf := range ufs {
		if uf.Name == backend {
			return c.DeleteBackendSwitchingRule(int64(i), frontend, t, 0)
		}
	}
	return nil
}

func (c *Client) createBckFrontendRels(name string, b *models.SiteFarm, edit bool, t string, p *parser.Parser) error {
	var res []error
	var err error
	if b.UseAs == "default" {
		if edit {
			err = c.removeUseFarm(name, b.Name, t, p)
			if err != nil {
				res = append(res, err)
			}
		}
		err = c.addDefaultBckToFrontend(name, b.Name, t, p)
		if err != nil {
			res = append(res, err)
		}
	} else {
		if b.Cond == "" || b.CondTest == "" {
			res = append(res, fmt.Errorf("Backend %s set as conditional but no conditions provided", b.Name))
		} else {
			i := int64(0)
			uf := &models.BackendSwitchingRule{
				ID:       &i,
				Name:     b.Name,
				Cond:     b.Cond,
				CondTest: b.CondTest,
			}
			err = c.CreateBackendSwitchingRule(name, uf, t, 0)
			if err != nil {
				res = append(res, err)
			}
		}
	}
	if len(res) > 0 {
		return CompositeTransactionError(res...)
	}
	return nil
}

func (c *Client) addDefaultBckToFrontend(fName string, bName string, t string, p *parser.Parser) error {
	frontend := &models.Frontend{Name: fName}

	if err := c.parseSection(frontend, parser.Frontends, fName, p); err != nil {
		return err
	}
	frontend.DefaultBackend = bName
	if err := c.EditFrontend(fName, frontend, t, 0); err != nil {
		return err
	}
	return nil
}

func (c *Client) removeDefaultBckToFrontend(fName string, t string, p *parser.Parser) error {
	frontend := &models.Frontend{Name: fName}
	if err := c.parseSection(frontend, parser.Frontends, fName, p); err != nil {
		return err
	}
	frontend.DefaultBackend = ""
	if err := c.EditFrontend(fName, frontend, t, 0); err != nil {
		return err
	}
	return nil
}

func (c *Client) editService(name string, service *models.SiteService, t string, p *parser.Parser) error {
	frontend := &models.Frontend{Name: name}
	if err := c.parseSection(frontend, parser.Frontends, name, p); err != nil {
		return err
	}

	frontend.HTTPConnectionMode = service.HTTPConnectionMode
	frontend.Maxconn = service.Maxconn
	frontend.Mode = service.Mode

	if err := c.EditFrontend(name, frontend, t, 0); err != nil {
		return err
	}
	return nil
}

func (c *Client) editFarm(name string, farm *models.SiteFarm, t string, p *parser.Parser) error {
	backend := &models.Backend{Name: name}
	if err := c.parseSection(backend, parser.Backends, name, p); err != nil {
		return err
	}

	backend.Mode = farm.Mode
	backend.Forwardfor = farm.Forwardfor
	backend.Balance = farm.Balance

	if err := c.EditBackend(name, backend, t, 0); err != nil {
		return err
	}
	return nil
}
