# The following lines of boilerplate have to be in your project's
# CMakeLists in this exact order for cmake to work correctly
cmake_minimum_required(VERSION 3.5)

include($ENV{IDF_PATH}/tools/cmake/project.cmake)
project(core-system)

# Git Version
#
# 起動ログに出す commit id (docs/game_session_design.md §4.0)。
# ファームは現物を見ても「どのソースから焼いたか」が分からないため、
# ビルド時に埋め込む。game-server の [boot] commit と同じ狙い (ADR P-7)。
#
# --dirty が付いていたら未コミットの変更が入ったビルド。
# --always でタグが1つも無くても短縮ハッシュを返す。
# main/version.h は生成物なので gitignore してある。
execute_process(COMMAND git describe --dirty --always --tags
                OUTPUT_VARIABLE GIT_VERSION
                OUTPUT_STRIP_TRAILING_WHITESPACE)
configure_file("main/version.h.in" "../main/version.h")
