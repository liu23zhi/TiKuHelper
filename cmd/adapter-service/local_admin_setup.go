package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/itihey/tikuAdapter/internal/registry"
	"golang.org/x/term"
)

func runLocalAdminSetup(input *os.File, output io.Writer) error {
	cfg, err := registry.ConfigWithError()
	if err != nil {
		return errors.New("无法读取服务配置")
	}
	tty := term.IsTerminal(int(input.Fd()))
	scan := bufio.NewScanner(input)
	scan.Buffer(make([]byte, 256), 256)
	read := func(prompt string) (string, error) {
		if tty {
			fmt.Fprint(output, prompt)
			bytes, err := term.ReadPassword(int(input.Fd()))
			fmt.Fprintln(output)
			if err != nil {
				return "", errors.New("无法从终端读取密码")
			}
			return string(bytes), nil
		}
		if !scan.Scan() {
			return "", errors.New("请通过标准输入分别提供当前初始化口令、新密码、再次输入的新密码（三行）")
		}
		return scan.Text(), nil
	}
	current, err := read("当前初始化口令（不回显）：")
	if err != nil {
		return err
	}
	next, err := read("设置新密码（不回显）：")
	if err != nil {
		return err
	}
	confirm, err := read("再次输入新密码（不回显）：")
	if err != nil {
		return err
	}
	if next != confirm {
		return errors.New("两次新密码不一致，未修改")
	}
	if err = registry.CompleteLocalAdminSetup(cfg, current, next); err != nil {
		return err
	}
	fmt.Fprintln(output, "管理员初始设置完成；请使用新密码登录。")
	return nil
}
