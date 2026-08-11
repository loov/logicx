#!/bin/zsh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Prints the version to stamp into a build: the version tag when the commit
# has one, otherwise a commit hash, so a development build never claims to be
# a release.
set -eu

cd "$(dirname "$0")"
describe=$(git describe --tags --always --dirty 2>/dev/null) || describe=unknown
echo "${describe#v}"
