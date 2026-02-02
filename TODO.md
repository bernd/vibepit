TODO
====

- Rename `x-vibepit` labels to `vibepit` or `dev.vibepit`
- We can't mount the vibepit binary into the proxy container on macOS.
  - Need another custom container image?
  - Embed the Linux binary into the macOS binary?
- Is it a problem that the network configuration in `.vibepit/network.yaml`
  is visible to the agents?
- Allow global preset configuration again.
- Start website and documentation
  - Check if there are tech-writer skills for Claude Code
  - Let Claude generate an architecture diagram and documentation
- Introduce `$XDG_CONFIG_DIR/vibepit/skills` directory that is mounted into
  a good location in the sandbox.
  - What's a good location? `~/.claude/skills`?
  - Read-only mount? What if we want to add more inside the container?
  - Or just copy the skills on every start?
- Adjust domain patterns to implicitly use :443 for each domain. Only allow HTTPS
  only allow HTTP if explicitly added with :80 (brainstorm on this)
- The monitor should allow log filtering to only show failed network connections
  and DNS lookups. (switchable via keypress)
- The log monitor should collapse the same messages. Agents usually the same
  host over and over again. We can just aggregate, show the latest timestamp
  and say "(10 previous identical messages)" Only when a new host is logged
  we start again.
- Add -H flag to run to allow using a non-shared home. On startup check if
  there's a non-shared volume for the project dir and use that.
- Allow searching for domains in the network setup UI to let users check
  if something is included or in which preset something can be found.
- Add a nicely-colored PS1 prompt to the sandbox shell. (use theme colors)
- Account the traffic bytes for each contacted hostname in memory. Allows
  showing traffic information and accounting of LLM inference payload size.
- How to access host services? Via proxy and custom DNS entries
- Check access to docker before running (group)
  - Should be okay on macOS
- Add docker-in-docker support to the sandbox.
  - The docker-in-docker container should also not be connected to the internet
    - Otherwise, this would be a way for the sandbox to escape
  - Have the proxy container proxy requests from the docker-in-docker container
- Let the proxy return a better message that describes why the connect attempt
  was denied. Maybe that helps agents to tell users what to do.
  ("connection denied to to firewall policy - use vibepit add or monitor to allow...")
