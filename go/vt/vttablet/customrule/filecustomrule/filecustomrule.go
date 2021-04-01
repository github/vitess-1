/*
Copyright 2019 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package filecustomrule implements static custom rule from a config file
package filecustomrule

import (
	"flag"
	"io/ioutil"
	"time"

	"github.com/falun/watch"

	"vitess.io/vitess/go/vt/log"
	"vitess.io/vitess/go/vt/vttablet/tabletserver"
	"vitess.io/vitess/go/vt/vttablet/tabletserver/rules"
)

var (
	// Actual FileCustomRule object in charge of rule updates
	fileCustomRule = NewFileCustomRule()
	// Commandline flag to specify rule path
	fileRulePath = flag.String("filecustomrules", "", "file based custom rule path")

	fileRulePollInterval = flag.Duration("filecustomrules_interval", 0, "How often should we poll the rule file <= 0 means we will not poll")
)

// FileCustomRule is an implementation of CustomRuleManager, it reads custom query
// rules from local file for once and push it to vttablet
type FileCustomRule struct {
	path                    string       // Path to the file containing custom query rules
	currentRuleSet          *rules.Rules // Query rules built from local file
	currentRuleSetTimestamp int64        // Unix timestamp when currentRuleSet is built from local file
}

// FileCustomRuleSource is the name of the file based custom rule source
const FileCustomRuleSource string = "FILE_CUSTOM_RULE"

// NewFileCustomRule returns pointer to new FileCustomRule structure
func NewFileCustomRule() (fcr *FileCustomRule) {
	fcr = new(FileCustomRule)
	fcr.path = ""
	fcr.currentRuleSet = rules.New()
	return fcr
}

// ParseRules will construct a Rules object based on a file path. In the case
// of error it returns nil and that error. A log will be printed to capture the
// stage at which parsing failed.
func ParseRules(path string) (*rules.Rules, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		log.Warningf("Error reading file %v: %v", path, err)
		// Don't update any internal cache, just return error
		return nil, err
	}
	qrs := rules.New()
	err = qrs.UnmarshalJSON(data)
	if err != nil {
		log.Warningf("Error unmarshaling query rules %v", err)
		return nil, err
	}
	return qrs, nil
}

// Open try to build query rules from local file and push the rules to vttablet
func (fcr *FileCustomRule) Open(qsc tabletserver.Controller, rulePath string) error {
	fcr.path = rulePath
	if fcr.path == "" {
		// Don't go further if path is empty
		return nil
	}

	qrs, err := ParseRules(rulePath)
	if err != nil {
		return err
	}
	fcr.currentRuleSetTimestamp = time.Now().Unix()
	fcr.currentRuleSet = qrs.Copy()
	// Push query rules to vttablet
	qsc.SetQueryRules(FileCustomRuleSource, qrs.Copy())
	log.Infof("Custom rule loaded from file: %s", fcr.path)
	return nil
}

// GetRules returns query rules built from local file
func (fcr *FileCustomRule) GetRules() (qrs *rules.Rules, version int64, err error) {
	return fcr.currentRuleSet.Copy(), fcr.currentRuleSetTimestamp, nil
}

// ActivateFileCustomRules activates this static file based custom rule mechanism
func ActivateFileCustomRules(qsc tabletserver.Controller) {
	if *fileRulePath != "" {
		qsc.RegisterQueryRuleSource(FileCustomRuleSource)
		fileCustomRule.Open(qsc, *fileRulePath)
		if *fileRulePollInterval > time.Duration(0) {
			w := watch.File(*fileRulePath)
			ch, cancelFn := w.OnInterval(*fileRulePollInterval)
			servenv.OnTerm(cancelFn)
			go func(tsc tabletserver.Controller) {
				for range ch {
					if err := fileCustomRule.Open(tsc, *fileRulePath); err != nil {
						log.Infof("Failed to load fileCustomRule %q: %v", *fileRulePath, err)
					} else {
						log.Infof("Opened %q", *fileRulePath)
					}
				}
			}(qsc)
		}
	}
}

func init() {
	tabletserver.RegisterFunctions = append(tabletserver.RegisterFunctions, ActivateFileCustomRules)
}
